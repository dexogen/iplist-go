package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDNSPartialFailuresRetainEachFamily(t *testing.T) {
	cfg := testLiveConfig(t)
	cfg.DNSRefreshIPv4 = true
	cfg.DNSRefreshIPv6 = true
	cfg.DNSRefreshConcurrency = 2
	cfg.DNSStaleAfter = 24 * time.Hour
	store := NewDNSRuntimeStore(cfg, quietLogger())
	site := Site{Name: "example.org", Group: "tools", Domains: []string{"one.example.org", "two.example.org"}}
	u := NewDNSUpdater(cfg, nil, store, quietLogger())
	u.lookup = func(ctx context.Context, family, domain string) ([]net.IP, error) {
		if family == "ip4" {
			return []net.IP{net.ParseIP("1.1.1.1")}, nil
		}
		return []net.IP{net.ParseIP("2606:4700:4700::1111")}, nil
	}
	first := u.resolveSite(context.Background(), "main", site)
	if first.err != nil {
		t.Fatal(first.err)
	}
	u = NewDNSUpdater(cfg, nil, store, quietLogger())
	u.lookup = func(ctx context.Context, family, domain string) ([]net.IP, error) {
		if family == "ip6" || domain == "two.example.org" {
			return nil, fmt.Errorf("timeout")
		}
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	second := u.resolveSite(context.Background(), "main", site)
	if second.err != nil || len(second.ip4) != 2 || len(second.ip6) != 1 || second.failed != 3 || second.retained != 3 {
		t.Fatalf("partial lookup lost answers: %+v", second)
	}
}

func TestDNSNegativeRequiresConfirmationAndStaleAnswersExpire(t *testing.T) {
	cfg := testLiveConfig(t)
	cfg.DNSRefreshIPv4 = true
	cfg.DNSRefreshConcurrency = 1
	cfg.DNSStaleAfter = time.Hour
	store := NewDNSRuntimeStore(cfg, quietLogger())
	site := Site{Name: "example.org", Group: "tools", Domains: []string{"example.org"}}
	run := func(lookup func(context.Context, string, string) ([]net.IP, error)) resolvedDomain {
		u := NewDNSUpdater(cfg, nil, store, quietLogger())
		u.lookup = lookup
		return u.resolveSite(context.Background(), "main", site)
	}
	positive := func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP("1.1.1.1")}, nil }
	negative := func(context.Context, string, string) ([]net.IP, error) { return nil, &net.DNSError{IsNotFound: true} }
	run(positive)
	if r := run(negative); len(r.ip4) != 1 {
		t.Fatal("first negative erased known answer")
	}
	if r := run(negative); len(r.ip4) != 0 {
		t.Fatal("confirmed negative did not clear answer")
	}
	run(positive)
	path := filepath.Join(store.root, ".domains", "main", "example.org.json")
	body, _ := os.ReadFile(path)
	var records domainRecords
	json.Unmarshal(body, &records)
	record := records.Domains["example.org"]
	record.Success4 = time.Now().Add(-2 * time.Hour)
	records.Domains["example.org"] = record
	body, _ = json.Marshal(records)
	atomicWrite(path, body)
	if r := run(func(context.Context, string, string) ([]net.IP, error) { return nil, fmt.Errorf("timeout") }); len(r.ip4) != 0 {
		t.Fatal("expired answer was retained")
	}
}

func TestDNSFamiliesHaveIndependentTimeouts(t *testing.T) {
	cfg := testLiveConfig(t)
	cfg.DNSRefreshIPv4 = true
	cfg.DNSRefreshIPv6 = true
	cfg.DNSRefreshResolveTimeout = 20 * time.Millisecond
	u := NewDNSUpdater(cfg, nil, NewDNSRuntimeStore(cfg, quietLogger()), quietLogger())
	u.lookup = func(ctx context.Context, family, domain string) ([]net.IP, error) {
		if family == "ip4" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []net.IP{net.ParseIP("2606:4700:4700::1111")}, nil
	}
	r := u.resolveDomain(context.Background(), "example.org", nil)
	if r.complete4 || !r.complete6 || len(r.ip6) != 1 {
		t.Fatalf("IPv4 timeout affected IPv6: %+v", r)
	}
}

func TestDNSCanceledRunDoesNotReplaceCache(t *testing.T) {
	cfg := testLiveConfig(t)
	cfg.DNSRefreshIPv4 = true
	cfg.DNSRefreshConcurrency = 1
	store := NewDNSRuntimeStore(cfg, quietLogger())
	u := NewDNSUpdater(cfg, nil, store, quietLogger())
	u.lookup = func(ctx context.Context, _, _ string) ([]net.IP, error) { return nil, ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := u.resolveSite(ctx, "main", Site{Name: "example.org", Group: "tools", Domains: []string{"example.org"}})
	if r.err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(filepath.Join(store.root, ".domains", "main", "example.org.json")); !os.IsNotExist(err) {
		t.Fatal("canceled run wrote cache")
	}
}

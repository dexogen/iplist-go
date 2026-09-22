package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func snapshotFixture(ip string) (SnapshotManifest, map[string][]byte) {
	manifest := SnapshotManifest{SchemaVersion: 1, PublishedAt: time.Now().UTC(), Sets: map[string]SnapshotDescriptor{}}
	files := map[string][]byte{}
	for _, key := range []string{"main", "beta", "russia"} {
		site := snapshotSite{Name: "example.org", Group: "tools", siteConfig: siteConfig{Domains: []string{"example.org"}, IP4: []string{ip}, IP6: []string{}, CIDR4: []string{}, CIDR6: []string{}}}
		raw, _ := json.Marshal(snapshotPayload{SchemaVersion: 1, ConfigSet: key, Sites: []snapshotSite{site}})
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(raw)
		w.Close()
		body := buf.Bytes()
		hash := sha256.Sum256(body)
		digest := hex.EncodeToString(hash[:])
		desc := SnapshotDescriptor{Path: "objects/" + digest + ".json.gz", SHA256: digest, Bytes: int64(len(body)), UnpackedBytes: int64(len(raw)), Sites: 1, Counts: map[string]int{"domains": 1, "ip4": 1, "ip6": 0, "cidr4": 0, "cidr6": 0}, Status: "ok", LastSuccessAt: time.Now().UTC(), CheckedAt: time.Now().UTC()}
		manifest.Sets[key] = desc
		files["/"+desc.Path] = body
	}
	files["/manifest.json"], _ = json.Marshal(manifest)
	return manifest, files
}

func testLiveConfig(t *testing.T) Config {
	t.Helper()
	return Config{DataRoot: t.TempDir(), SnapshotDir: "runtime/snapshots", ExportCacheDir: "runtime/export-cache", DNSRuntimeDir: "runtime/dns", SnapshotTimeout: time.Second * 5, SnapshotMaxAge: 48 * time.Hour}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLiveRefreshChangesExportsAndRecoversOffline(t *testing.T) {
	first, files := snapshotFixture("1.1.1.1")
	var mu sync.RWMutex
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	cfg := testLiveConfig(t)
	cfg.SnapshotURL = origin.URL + "/manifest.json"
	live := NewLiveServer(cfg, quietLogger())
	if err := live.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	check := func(want, hash string) {
		t.Helper()
		r := httptest.NewRecorder()
		live.ServeHTTP(r, httptest.NewRequest("GET", "/api/latest/export?format=unifi&data=ipv4", nil))
		if r.Code != 200 || !strings.Contains(r.Body.String(), want) || r.Header().Get("X-IPList-Snapshot") != hash || r.Header().Get("X-IPList-Export-Cache") != "hit" {
			t.Fatalf("unexpected response: %d %s %v", r.Code, r.Body, r.Header())
		}
	}
	check("1.1.1.1/32", first.Sets["main"].SHA256)
	second, next := snapshotFixture("8.8.8.8")
	mu.Lock()
	files = next
	mu.Unlock()
	if err := live.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("8.8.8.8/32", second.Sets["main"].SHA256)
	mu.Lock()
	files["/manifest.json"] = []byte("<html>unavailable</html>")
	mu.Unlock()
	if err := live.refresh(context.Background()); err == nil {
		t.Fatal("expected manifest failure")
	}
	check("8.8.8.8/32", second.Sets["main"].SHA256)
	if live.Status().LastError == "" {
		t.Fatal("missing error status")
	}
	origin.Close()
	restarted := NewLiveServer(cfg, quietLogger())
	if err := restarted.loadSaved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.data().Revision != snapshotRevision(&second) {
		t.Fatal("offline restart lost latest snapshot")
	}
	if err := os.WriteFile(filepath.Join(restarted.client.root, "current.json"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	recovery := NewLiveServer(cfg, quietLogger())
	if err := recovery.loadSaved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recovery.data().Revision != snapshotRevision(&first) {
		t.Fatal("failed to recover previous generation")
	}
}

func TestSnapshotRejectsCorruptionAndCountMismatch(t *testing.T) {
	manifest, files := snapshotFixture("1.1.1.1")
	desc := manifest.Sets["main"]
	body := files["/"+desc.Path]
	if _, err := decodeSnapshot(append(append([]byte{}, body...), 0), "main", desc); err == nil {
		t.Fatal("accepted checksum mismatch")
	}
	desc.Counts["ip4"] = 2
	if _, err := decodeSnapshot(body, "main", desc); err == nil {
		t.Fatal("accepted count mismatch")
	}
	delete(manifest.Sets, "russia")
	raw, _ := json.Marshal(manifest)
	if _, err := parseManifest(raw); err == nil {
		t.Fatal("accepted missing set")
	}
}

func TestConcurrentExportsRemainOnOneGeneration(t *testing.T) {
	first, files := snapshotFixture("1.1.1.1")
	var mu sync.RWMutex
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		w.Write(files[r.URL.Path])
	}))
	defer origin.Close()
	cfg := testLiveConfig(t)
	cfg.SnapshotURL = origin.URL + "/manifest.json"
	live := NewLiveServer(cfg, quietLogger())
	if err := live.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, next := snapshotFixture("8.8.8.8")
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				r := httptest.NewRecorder()
				live.ServeHTTP(r, httptest.NewRequest("GET", "/api/latest/export?format=unifi&data=ipv4", nil))
				hash := r.Header().Get("X-IPList-Snapshot")
				want := ""
				if hash == first.Sets["main"].SHA256 {
					want = "1.1.1.1/32"
				}
				if hash == second.Sets["main"].SHA256 {
					want = "8.8.8.8/32"
				}
				if r.Code != 200 || want == "" || strings.TrimSpace(r.Body.String()) != want {
					t.Errorf("mixed generation: %d %s %s", r.Code, hash, r.Body.String())
					return
				}
			}
		}()
	}
	mu.Lock()
	files = next
	mu.Unlock()
	if err := live.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestSnapshotAssetURLSupportsReleaseAndPages(t *testing.T) {
	desc := SnapshotDescriptor{Path: "objects/abc.json.gz"}
	for base, want := range map[string]string{"https://example.org/data/manifest.json": "https://example.org/data/objects/abc.json.gz", "https://github.com/a/b/releases/download/data/manifest.json": "https://github.com/a/b/releases/download/data/abc.json.gz"} {
		got, err := snapshotObjectURL(base, desc)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", base, got, err)
		}
	}
}

func TestDNSAndSnapshotRefreshCanOverlapWithRuntimeRequests(t *testing.T) {
	_, files := snapshotFixture("1.1.1.1")
	var mu sync.RWMutex
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		w.Write(files[r.URL.Path])
	}))
	defer origin.Close()
	cfg := testLiveConfig(t)
	cfg.SnapshotURL = origin.URL + "/manifest.json"
	cfg.DNSRefreshIPv4 = true
	cfg.DNSRefreshConcurrency = 1
	cfg.DNSRefreshResolveTimeout = 5 * time.Millisecond
	cfg.DNSServers = []string{"127.0.0.1:1"}
	live := NewLiveServer(cfg, quietLogger())
	if err := live.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 6; i++ {
			_ = live.runDNS(context.Background())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			r := httptest.NewRecorder()
			live.ServeHTTP(r, httptest.NewRequest("GET", "/api/latest/runtime", nil))
			if r.Code != 200 {
				t.Errorf("runtime unavailable: %d", r.Code)
			}
		}
	}()
	for i := 0; i < 5; i++ {
		_, next := snapshotFixture(fmt.Sprintf("8.8.8.%d", i+1))
		mu.Lock()
		files = next
		mu.Unlock()
		if err := live.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("overlapping refresh deadlocked")
	}
}

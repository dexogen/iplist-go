package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type domainRecord struct {
	IP4       []string  `json:"ip4,omitempty"`
	IP6       []string  `json:"ip6,omitempty"`
	Success4  time.Time `json:"success4,omitempty"`
	Success6  time.Time `json:"success6,omitempty"`
	Negative4 int       `json:"negative4,omitempty"`
	Negative6 int       `json:"negative6,omitempty"`
}

type domainRecords struct {
	Policy    string                  `json:"policy"`
	Domains   map[string]domainRecord `json:"domains"`
	LegacyIP4 []string                `json:"legacy_ip4,omitempty"`
	LegacyIP6 []string                `json:"legacy_ip6,omitempty"`
	LegacyAt  time.Time               `json:"legacy_at,omitempty"`
}

func (u *DNSUpdater) resolveSite(ctx context.Context, configSet string, site Site) resolvedDomain {
	servers := u.cfg.DNSServers
	if u.cfg.DNSUseSourceServers && len(servers) == 0 {
		servers = site.DNS
	}
	policyBytes := sha256.Sum256([]byte(strings.Join(servers, "\x00")))
	policy := hex.EncodeToString(policyBytes[:])
	path := filepath.Join(u.store.root, ".domains", configSet, site.Name+".json")
	records := domainRecords{Policy: policy, Domains: map[string]domainRecord{}}
	if body, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(body, &records); err != nil {
			return resolvedDomain{err: fmt.Errorf("load domain cache: %w", err)}
		}
		if records.Policy != policy {
			records = domainRecords{Policy: policy, Domains: map[string]domainRecord{}}
		}
	} else if !os.IsNotExist(err) {
		return resolvedDomain{err: err}
	} else {
		old := u.store.Additions(configSet)[site.Name]
		records.LegacyIP4 = old.IP4
		records.LegacyIP6 = old.IP6
		records.LegacyAt, _ = time.Parse(time.RFC3339, old.UpdatedAt)
	}
	if records.Domains == nil {
		records.Domains = map[string]domainRecord{}
	}
	domains := normalizeResolveDomains(site.Domains)
	concurrency := u.cfg.DNSRefreshConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(domains) {
		concurrency = len(domains)
	}
	jobs := make(chan string)
	type result struct {
		name     string
		resolved resolvedDomain
	}
	results := make(chan result)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for name := range jobs {
				r := u.cachedResolve(ctx, name, servers)
				select {
				case results <- result{name, r}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, name := range domains {
			select {
			case jobs <- name:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	output := resolvedDomain{ok: true}
	next := map[string]domainRecord{}
	now := time.Now().UTC()
	staleAfter := u.cfg.DNSStaleAfter
	if staleAfter <= 0 {
		staleAfter = 7 * 24 * time.Hour
	}
	for result := range results {
		record := records.Domains[result.name]
		for _, family := range []struct {
			enabled, complete, negative bool
			ips                         []string
			stored                      *[]string
			at                          *time.Time
			negatives                   *int
		}{
			{u.cfg.DNSRefreshIPv4, result.resolved.complete4, result.resolved.negative4, result.resolved.ip4, &record.IP4, &record.Success4, &record.Negative4},
			{u.cfg.DNSRefreshIPv6, result.resolved.complete6, result.resolved.negative6, result.resolved.ip6, &record.IP6, &record.Success6, &record.Negative6},
		} {
			if !family.enabled {
				continue
			}
			if family.complete {
				if family.negative {
					*family.negatives++
				} else {
					*family.negatives = 0
				}
				if !family.negative || *family.negatives >= 2 {
					*family.stored = family.ips
					*family.at = now
				}
			} else {
				output.failed++
				output.ok = false
			}
			if !family.at.IsZero() && now.Sub(*family.at) > staleAfter {
				*family.stored = nil
			}
			if !family.complete && len(*family.stored) > 0 {
				output.retained++
			}
		}
		next[result.name] = record
		if u.cfg.DNSRefreshIPv4 {
			output.ip4 = append(output.ip4, record.IP4...)
		}
		if u.cfg.DNSRefreshIPv6 {
			output.ip6 = append(output.ip6, record.IP6...)
		}
	}
	if err := ctx.Err(); err != nil {
		return resolvedDomain{err: err}
	}
	records.Domains = next
	if output.ok || now.Sub(records.LegacyAt) > staleAfter {
		records.LegacyIP4 = nil
		records.LegacyIP6 = nil
	}
	if u.cfg.DNSRefreshIPv4 {
		output.ip4 = append(output.ip4, records.LegacyIP4...)
	}
	if u.cfg.DNSRefreshIPv6 {
		output.ip6 = append(output.ip6, records.LegacyIP6...)
	}
	output.ip4 = normalizeStrings(output.ip4)
	output.ip6 = normalizeStrings(output.ip6)
	body, err := json.Marshal(records)
	if err == nil {
		err = atomicWrite(path, body)
	}
	output.err = err
	return output
}

func (u *DNSUpdater) cachedResolve(ctx context.Context, domain string, servers []string) resolvedDomain {
	key := strings.Join(servers, "\x00") + "\x01" + domain
	u.memoMu.Lock()
	previous, ok := u.memo[key]
	u.memoMu.Unlock()
	if ok {
		return previous
	}
	result := u.resolveDomain(ctx, domain, servers)
	if ctx.Err() == nil {
		u.memoMu.Lock()
		if u.memo == nil {
			u.memo = map[string]resolvedDomain{}
		}
		u.memo[key] = result
		u.memoMu.Unlock()
	}
	return result
}

func (u *DNSUpdater) resolveDomain(parent context.Context, domain string, servers []string) resolvedDomain {
	resolver := net.DefaultResolver
	if len(servers) > 0 {
		resolver = resolverForServers(servers)
	}
	lookup := u.lookup
	if lookup == nil {
		lookup = resolver.LookupIP
	}
	type response struct {
		family             int
		ips                []string
		complete, negative bool
	}
	results := make(chan response, 2)
	count := 0
	for _, family := range []int{4, 6} {
		if family == 4 && !u.cfg.DNSRefreshIPv4 || family == 6 && !u.cfg.DNSRefreshIPv6 {
			continue
		}
		count++
		go func(family int) {
			timeout := u.cfg.DNSRefreshResolveTimeout
			if timeout <= 0 {
				timeout = 4 * time.Second
			}
			ctx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()
			ips, err := lookup(ctx, fmt.Sprintf("ip%d", family), domain)
			var dnsErr *net.DNSError
			negative := errors.As(err, &dnsErr) && dnsErr.IsNotFound
			results <- response{family, parseResolvedIPs(ips, family == 4), err == nil || negative, negative}
		}(family)
	}
	var output resolvedDomain
	for i := 0; i < count; i++ {
		r := <-results
		if r.family == 4 {
			output.ip4 = r.ips
			output.complete4 = r.complete
			output.negative4 = r.negative
		} else {
			output.ip6 = r.ips
			output.complete6 = r.complete
			output.negative6 = r.negative
		}
	}
	output.ok = (!u.cfg.DNSRefreshIPv4 || output.complete4) && (!u.cfg.DNSRefreshIPv6 || output.complete6)
	return output
}

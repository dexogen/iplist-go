package app

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type DNSUpdater struct {
	cfg    Config
	data   *AllData
	store  *DNSRuntimeStore
	logger *slog.Logger
}

type resolvedDomain struct {
	ip4 []string
	ip6 []string
	ok  bool
}

func NewDNSUpdater(cfg Config, data *AllData, store *DNSRuntimeStore, logger *slog.Logger) *DNSUpdater {
	return &DNSUpdater{cfg: cfg, data: data, store: store, logger: logger}
}

func (u *DNSUpdater) Start(ctx context.Context) {
	if !u.cfg.DNSRefreshEnabled {
		u.logger.Info("dns refresh disabled")
		return
	}
	schedule, err := parseCronSchedule(u.cfg.DNSRefreshCron)
	if err != nil {
		u.logger.Error("invalid dns refresh cron", "cron", u.cfg.DNSRefreshCron, "error", err)
		u.setStatusForAll(func(status DNSRefreshStatus) DNSRefreshStatus {
			status.Enabled = true
			status.LastError = err.Error()
			return status
		})
		return
	}
	location, err := time.LoadLocation(u.cfg.DNSRefreshTimezone)
	if err != nil {
		u.logger.Error("invalid dns refresh timezone", "timezone", u.cfg.DNSRefreshTimezone, "error", err)
		u.setStatusForAll(func(status DNSRefreshStatus) DNSRefreshStatus {
			status.Enabled = true
			status.LastError = err.Error()
			return status
		})
		return
	}
	go u.loop(ctx, schedule, location)
}

func (u *DNSUpdater) loop(ctx context.Context, schedule *cronSchedule, location *time.Location) {
	for {
		next := schedule.Next(time.Now().In(location))
		if next.IsZero() {
			u.setStatusForAll(func(status DNSRefreshStatus) DNSRefreshStatus {
				status.Enabled = true
				status.LastError = "could not calculate next dns refresh run"
				return status
			})
			return
		}
		u.setStatusForAll(func(status DNSRefreshStatus) DNSRefreshStatus {
			status.Enabled = true
			status.NextRunAt = next.UTC().Format(time.RFC3339)
			return status
		})

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			u.runAllSets(ctx)
		}
	}
}

func (u *DNSUpdater) runAllSets(ctx context.Context) {
	for _, configSet := range u.data.ConfigSetKeys() {
		if err := u.runSet(ctx, configSet); err != nil {
			u.logger.Warn("dns refresh set failed", "configSet", configSet, "error", err)
			u.store.SetStatus(configSet, func(status DNSRefreshStatus) DNSRefreshStatus {
				status.Running = false
				status.LastError = err.Error()
				return status
			})
		}
	}
}

func (u *DNSUpdater) runSet(ctx context.Context, configSet string) error {
	base := u.data.Sets[configSet]
	if base == nil {
		return nil
	}
	started := time.Now().UTC().Format(time.RFC3339)
	u.store.SetStatus(configSet, func(status DNSRefreshStatus) DNSRefreshStatus {
		status.Enabled = true
		status.Running = true
		status.CurrentSite = ""
		status.Processed = 0
		status.Total = len(base.Sites)
		status.Resolved = 0
		status.Added = 0
		status.LastStartedAt = started
		status.LastFinishedAt = ""
		status.LastError = ""
		return status
	})

	for i, site := range base.Sites {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		u.store.SetStatus(configSet, func(status DNSRefreshStatus) DNSRefreshStatus {
			status.CurrentSite = site.Name
			status.Processed = i
			return status
		})
		resolved := u.resolveSite(ctx, site)
		additions := DNSRuntimeAdditions{
			IP4: filterAdditionalIPs(site.IP4, site.CIDR4, resolved.ip4),
			IP6: filterAdditionalIPs(site.IP6, site.CIDR6, resolved.ip6),
		}
		if resolved.ok || len(site.Domains) == 0 {
			if err := u.store.SetAdditions(configSet, site, additions); err != nil {
				return err
			}
		}
		added := len(additions.IP4) + len(additions.IP6)
		u.store.SetStatus(configSet, func(status DNSRefreshStatus) DNSRefreshStatus {
			status.Processed = i + 1
			status.Resolved += resolved.count()
			status.Added += added
			return status
		})
	}

	finished := time.Now().UTC().Format(time.RFC3339)
	u.store.SetStatus(configSet, func(status DNSRefreshStatus) DNSRefreshStatus {
		status.Running = false
		status.CurrentSite = ""
		status.Processed = len(base.Sites)
		status.Total = len(base.Sites)
		status.LastFinishedAt = finished
		return status
	})
	if u.data.ExportCache != nil {
		if err := u.data.ExportCache.RefreshSet(configSet); err != nil {
			u.logger.Warn("export cache refresh failed", "configSet", configSet, "error", err)
		}
	}
	return nil
}

func (u *DNSUpdater) setStatusForAll(update func(DNSRefreshStatus) DNSRefreshStatus) {
	for _, configSet := range u.data.ConfigSetKeys() {
		u.store.SetStatus(configSet, update)
	}
}

func (u *DNSUpdater) resolveSite(ctx context.Context, site Site) resolvedDomain {
	domains := normalizeResolveDomains(site.Domains)
	if len(domains) == 0 {
		return resolvedDomain{ok: true}
	}

	concurrency := u.cfg.DNSRefreshConcurrency
	if concurrency > len(domains) {
		concurrency = len(domains)
	}
	jobs := make(chan string)
	results := make(chan resolvedDomain)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for domain := range jobs {
				results <- u.resolveDomain(ctx, domain, site.DNS)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, domain := range domains {
			select {
			case <-ctx.Done():
				return
			case jobs <- domain:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var output resolvedDomain
	for result := range results {
		output.ip4 = append(output.ip4, result.ip4...)
		output.ip6 = append(output.ip6, result.ip6...)
		output.ok = output.ok || result.ok
	}
	output.ip4 = normalizeStrings(output.ip4)
	output.ip6 = normalizeStrings(output.ip6)
	return output
}

func (u *DNSUpdater) resolveDomain(parent context.Context, domain string, servers []string) resolvedDomain {
	ctx, cancel := context.WithTimeout(parent, u.cfg.DNSRefreshResolveTimeout)
	defer cancel()

	resolver := net.DefaultResolver
	if len(servers) > 0 {
		resolver = resolverForServers(servers)
	}

	var output resolvedDomain
	var ok atomic.Bool
	if u.cfg.DNSRefreshIPv4 {
		if ips, err := resolver.LookupIP(ctx, "ip4", domain); err == nil {
			output.ip4 = append(output.ip4, parseResolvedIPs(ips, true)...)
			ok.Store(true)
		}
	}
	if u.cfg.DNSRefreshIPv6 {
		if ips, err := resolver.LookupIP(ctx, "ip6", domain); err == nil {
			output.ip6 = append(output.ip6, parseResolvedIPs(ips, false)...)
			ok.Store(true)
		}
	}
	output.ok = ok.Load()
	return output
}

func resolverForServers(servers []string) *net.Resolver {
	normalized := make([]string, 0, len(servers))
	for _, server := range servers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(server, "53")
		}
		normalized = append(normalized, server)
	}
	var next atomic.Uint64
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if len(normalized) == 0 {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, address)
			}
			server := normalized[int(next.Add(1)-1)%len(normalized)]
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, server)
		},
	}
}

func parseResolvedIPs(ips []net.IP, wantV4 bool) []string {
	output := make([]string, 0, len(ips))
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if wantV4 != addr.Is4() {
			continue
		}
		output = append(output, addr.String())
	}
	return output
}

func normalizeResolveDomains(domains []string) []string {
	output := make([]string, 0, len(domains))
	seen := map[string]struct{}{}
	for _, domain := range domains {
		domain = strings.TrimSpace(strings.TrimPrefix(domain, "*."))
		domain = strings.TrimPrefix(domain, ".")
		domain = strings.TrimSuffix(domain, ".")
		if domain == "" || strings.ContainsAny(domain, "/ :") {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		output = append(output, domain)
	}
	return output
}

func (r resolvedDomain) count() int {
	return len(r.ip4) + len(r.ip6)
}

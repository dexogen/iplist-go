package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type DNSRuntimeStore struct {
	mu        sync.RWMutex
	root      string
	enabled   bool
	logger    *slog.Logger
	additions map[string]map[string]DNSRuntimeAdditions
	status    map[string]DNSRefreshStatus
}

type DNSRuntimeAdditions struct {
	IP4         []string `json:"ip4"`
	IP6         []string `json:"ip6"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
	DomainsHash string   `json:"domainsHash,omitempty"`
}

type DNSRefreshStatus struct {
	Enabled        bool   `json:"enabled"`
	Running        bool   `json:"running"`
	CurrentSite    string `json:"currentSite,omitempty"`
	Processed      int    `json:"processed"`
	Total          int    `json:"total"`
	Resolved       int    `json:"resolved"`
	Added          int    `json:"added"`
	Failed         int    `json:"failed"`
	Retained       int    `json:"retained"`
	LastStartedAt  string `json:"lastStartedAt,omitempty"`
	LastFinishedAt string `json:"lastFinishedAt,omitempty"`
	NextRunAt      string `json:"nextRunAt,omitempty"`
	LastError      string `json:"lastError,omitempty"`
}

func NewDNSRuntimeStore(cfg Config, logger *slog.Logger) *DNSRuntimeStore {
	root := cfg.DNSRuntimeDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(cfg.DataRoot, root)
	}
	store := &DNSRuntimeStore{
		root:      root,
		enabled:   cfg.DNSRefreshEnabled,
		logger:    logger,
		additions: map[string]map[string]DNSRuntimeAdditions{},
		status:    map[string]DNSRefreshStatus{},
	}
	return store
}

func (s *DNSRuntimeStore) Load(data *AllData) error {
	for _, configSet := range data.ConfigSetKeys() {
		if _, ok := s.status[configSet]; !ok {
			s.status[configSet] = DNSRefreshStatus{Enabled: s.enabled}
		}
		if err := s.loadSet(configSet, data.Sets[configSet]); err != nil {
			return err
		}
		s.loadStatus(configSet)
		s.deriveStatusFromAdditions(configSet, data.Sets[configSet])
	}
	return nil
}

func (s *DNSRuntimeStore) loadSet(configSet string, data *ConfigSetData) error {
	dir := filepath.Join(s.root, configSet)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	known := map[string]string{}
	if data != nil {
		for _, site := range data.Sites {
			known[site.Name] = domainsHash(site.Domains)
		}
	}
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		if _, ok := known[name]; len(known) > 0 && !ok {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var additions DNSRuntimeAdditions
		if err := json.Unmarshal(content, &additions); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if additions.DomainsHash != "" && additions.DomainsHash != known[name] {
			return nil
		}
		if old, ok := s.additions[configSet][name]; ok && old.UpdatedAt > additions.UpdatedAt {
			return nil
		}
		s.setMemory(configSet, name, DNSRuntimeAdditions{
			IP4:         normalizeStrings(additions.IP4),
			IP6:         normalizeStrings(additions.IP6),
			UpdatedAt:   additions.UpdatedAt,
			DomainsHash: additions.DomainsHash,
		})
		return nil
	})
}

func (s *DNSRuntimeStore) Additions(configSet string) map[string]DNSRuntimeAdditions {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source := s.additions[configSet]
	output := make(map[string]DNSRuntimeAdditions, len(source))
	for name, additions := range source {
		output[name] = DNSRuntimeAdditions{
			IP4:         append([]string(nil), additions.IP4...),
			IP6:         append([]string(nil), additions.IP6...),
			UpdatedAt:   additions.UpdatedAt,
			DomainsHash: additions.DomainsHash,
		}
	}
	return output
}

func (s *DNSRuntimeStore) Status(configSet string) DNSRefreshStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status[configSet]
	return status
}

func (s *DNSRuntimeStore) SetStatus(configSet string, update func(DNSRefreshStatus) DNSRefreshStatus) {
	s.mu.Lock()
	status := s.status[configSet]
	if _, ok := s.status[configSet]; !ok {
		status.Enabled = s.enabled
	}
	status = update(status)
	s.status[configSet] = status
	s.mu.Unlock()

	if err := s.saveStatus(configSet, status); err != nil {
		s.logger.Warn("failed to persist dns refresh status", "configSet", configSet, "error", err)
	}
}

func (s *DNSRuntimeStore) SetAdditions(configSet string, site Site, additions DNSRuntimeAdditions) error {
	additions.IP4 = normalizeStrings(additions.IP4)
	additions.IP6 = normalizeStrings(additions.IP6)
	additions.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	additions.DomainsHash = domainsHash(site.Domains)
	path := filepath.Join(s.root, configSet, filepath.FromSlash(site.Group), site.Name+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(additions, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := atomicWrite(path, content); err != nil {
		return err
	}
	s.setMemory(configSet, site.Name, additions)
	return nil
}

func domainsHash(domains []string) string {
	digest := sha256.Sum256([]byte(strings.Join(normalizeStrings(domains), "\n")))
	return hex.EncodeToString(digest[:])
}

func (s *DNSRuntimeStore) statusPath(configSet string) string {
	return filepath.Join(s.root, ".status", configSet+".json")
}

func (s *DNSRuntimeStore) loadStatus(configSet string) {
	path := s.statusPath(configSet)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.logger.Warn("failed to read dns refresh status", "configSet", configSet, "error", err)
		return
	}

	var status DNSRefreshStatus
	if err := json.Unmarshal(content, &status); err != nil {
		s.logger.Warn("failed to parse dns refresh status", "configSet", configSet, "error", err)
		return
	}

	s.mu.Lock()
	enabled := s.status[configSet].Enabled
	status.Enabled = enabled
	status.Running = false
	status.CurrentSite = ""
	s.status[configSet] = status
	s.mu.Unlock()
}

func (s *DNSRuntimeStore) saveStatus(configSet string, status DNSRefreshStatus) error {
	status.Running = false
	status.CurrentSite = ""
	path := s.statusPath(configSet)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return atomicWrite(path, content)
}

func (s *DNSRuntimeStore) deriveStatusFromAdditions(configSet string, data *ConfigSetData) {
	s.mu.Lock()
	status := s.status[configSet]
	if status.LastFinishedAt != "" {
		s.mu.Unlock()
		return
	}

	additions := s.additions[configSet]
	var startedAt time.Time
	var finishedAt time.Time
	added := 0
	processed := 0
	for _, addition := range additions {
		if addition.UpdatedAt == "" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, addition.UpdatedAt)
		if err != nil {
			continue
		}
		if startedAt.IsZero() || updatedAt.Before(startedAt) {
			startedAt = updatedAt
		}
		if finishedAt.IsZero() || updatedAt.After(finishedAt) {
			finishedAt = updatedAt
		}
		processed++
		added += len(addition.IP4) + len(addition.IP6)
	}
	if finishedAt.IsZero() {
		s.mu.Unlock()
		return
	}

	status.Running = false
	status.CurrentSite = ""
	status.Processed = processed
	status.Total = processed
	if data != nil {
		status.Total = len(data.Sites)
	}
	status.Added = added
	status.LastStartedAt = startedAt.UTC().Format(time.RFC3339)
	status.LastFinishedAt = finishedAt.UTC().Format(time.RFC3339)
	s.status[configSet] = status
	s.mu.Unlock()

	if err := s.saveStatus(configSet, status); err != nil {
		s.logger.Warn("failed to persist derived dns refresh status", "configSet", configSet, "error", err)
	}
}

func (s *DNSRuntimeStore) setMemory(configSet, siteName string, additions DNSRuntimeAdditions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.additions[configSet] == nil {
		s.additions[configSet] = map[string]DNSRuntimeAdditions{}
	}
	s.additions[configSet][siteName] = additions
}

func (d *AllData) MergedConfigSet(configSet string) *ConfigSetData {
	if cached := d.MergedSets[configSet]; cached != nil {
		return cached
	}
	base := d.Sets[configSet]
	if base == nil {
		return nil
	}
	sites := d.MergedSites(configSet)
	return &ConfigSetData{
		ConfigSet: base.ConfigSet,
		Sites:     sites,
		Groups:    buildGroups(sites),
	}
}

func (d *AllData) MergedSites(configSet string) []Site {
	base := d.Sets[configSet]
	if base == nil {
		return nil
	}
	additions := map[string]DNSRuntimeAdditions{}
	if d.Runtime != nil {
		additions = d.Runtime.Additions(configSet)
	}
	output := make([]Site, 0, len(base.Sites))
	for _, site := range base.Sites {
		next := site
		next.DynamicCounts = nil
		next.DynamicTotal = 0

		if addition, ok := additions[site.Name]; ok {
			ip4 := filterAdditionalIPs(site.IP4, site.CIDR4, addition.IP4)
			ip6 := filterAdditionalIPs(site.IP6, site.CIDR6, addition.IP6)
			next.IP4 = mergeStrings(next.IP4, ip4)
			next.IP6 = mergeStrings(next.IP6, ip6)
			next.DynamicTotal = len(ip4) + len(ip6)
			if next.DynamicTotal > 0 {
				next.DynamicCounts = map[string]int{"ip4": len(ip4), "ip6": len(ip6)}
			}
		}
		output = append(output, next)
	}
	return output
}

func filterAdditionalIPs(baseIPs []string, baseCIDRs []string, candidates []string) []string {
	exact := map[string]struct{}{}
	for _, ip := range baseIPs {
		exact[ip] = struct{}{}
	}
	prefixes := make([]netip.Prefix, 0, len(baseCIDRs))
	for _, cidr := range baseCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	output := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	sharedAddressSpace := netip.MustParsePrefix("100.64.0.0/10")
	for _, candidate := range candidates {
		addr, err := netip.ParseAddr(candidate)
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		// Public service domains can return internal or blocking addresses. They
		// must not turn LAN destinations into VPN routes through DNS enrichment.
		if !addr.IsGlobalUnicast() || addr.IsPrivate() || sharedAddressSpace.Contains(addr) {
			continue
		}
		normalized := addr.String()
		if _, ok := exact[normalized]; ok {
			continue
		}
		covered := false
		for _, prefix := range prefixes {
			if prefix.Contains(addr) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		output = append(output, normalized)
	}
	sort.Strings(output)
	return output
}

func mergeStrings(base []string, additions []string) []string {
	if len(additions) == 0 {
		return base
	}
	return normalizeStrings(append(append([]string(nil), base...), additions...))
}

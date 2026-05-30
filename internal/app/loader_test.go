package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMainConfig(t *testing.T) {
	data, err := LoadData(Config{ConfigSet: "main", DataRoot: fixtureDataRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sites) == 0 {
		t.Fatal("expected sites")
	}
	if len(data.Groups) == 0 {
		t.Fatal("expected groups")
	}
}

func TestLoadAllConfigSets(t *testing.T) {
	data, err := LoadAllData(Config{DataRoot: fixtureDataRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, configSet := range []string{"main", "alpha", "beta", "russia"} {
		if data.Sets[configSet] == nil {
			t.Fatalf("missing %s config set", configSet)
		}
		if len(data.Sets[configSet].Sites) == 0 {
			t.Fatalf("expected sites in %s config set", configSet)
		}
	}
	if strings.Join(data.ConfigSetKeys(), ",") != "main,beta,russia,alpha" {
		t.Fatalf("config set order = %v", data.ConfigSetKeys())
	}
}

func TestAPIRoutesSelectConfigSets(t *testing.T) {
	server := &Server{data: &AllData{Sets: map[string]*ConfigSetData{
		"main":    {},
		"alpha":   {},
		"beta":    {},
		"dexlist": {},
		"russia":  {},
	}}}
	tests := map[string]struct {
		wantConfigSet string
		wantPrefix    string
		wantPath      string
	}{
		"/api/latest/":        {"main", "/api/latest", "/"},
		"/api/latest/catalog": {"main", "/api/latest", "/catalog"},
		"/api/latest/export":  {"main", "/api/latest", "/export"},
		"/api/alpha/catalog":  {"alpha", "/api/alpha", "/catalog"},
		"/api/beta/":          {"beta", "/api/beta", "/"},
		"/api/beta/export":    {"beta", "/api/beta", "/export"},
		"/api/dexlist/export": {"dexlist", "/api/dexlist", "/export"},
		"/api/russia/runtime": {"russia", "/api/russia", "/runtime"},
	}
	for path, want := range tests {
		gotConfigSet, gotPrefix, gotPath, ok := server.apiRoute(path)
		if !ok {
			t.Fatalf("%s was not routed as API", path)
		}
		if gotConfigSet != want.wantConfigSet || gotPrefix != want.wantPrefix || gotPath != want.wantPath {
			t.Fatalf("%s routed to (%s, %s, %s), want (%s, %s, %s)", path, gotConfigSet, gotPrefix, gotPath, want.wantConfigSet, want.wantPrefix, want.wantPath)
		}
	}
}

func TestUIRoutesSelectConfigSets(t *testing.T) {
	server := &Server{data: &AllData{Sets: map[string]*ConfigSetData{
		"main":    {},
		"alpha":   {},
		"beta":    {},
		"dexlist": {},
		"russia":  {},
	}}}
	tests := map[string]struct {
		wantConfigSet string
		wantPath      string
	}{
		"/":             {"main", "/"},
		"/about":        {"main", "/about"},
		"/alpha":        {"alpha", "/"},
		"/alpha/about":  {"alpha", "/about"},
		"/beta":         {"beta", "/"},
		"/beta/about":   {"beta", "/about"},
		"/dexlist":      {"dexlist", "/"},
		"/russia":       {"russia", "/"},
		"/russia/about": {"russia", "/about"},
	}
	for path, want := range tests {
		gotConfigSet, gotPath := server.uiRoute(path)
		if gotConfigSet != want.wantConfigSet || gotPath != want.wantPath {
			t.Fatalf("%s routed to (%s, %s), want (%s, %s)", path, gotConfigSet, gotPath, want.wantConfigSet, want.wantPath)
		}
	}
}

func TestPortalURLsAreRelative(t *testing.T) {
	got := portalURLs([]ConfigSetInfo{
		{Key: "main", URL: "/"},
		{Key: "alpha", URL: "/alpha"},
		{Key: "beta", URL: "/beta"},
		{Key: "russia", URL: "/russia"},
	})
	want := map[string]string{
		"master": "/",
		"main":   "/",
		"alpha":  "/alpha",
		"beta":   "/beta",
		"russia": "/russia",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("portalURLs[%s] = %q, want %q", key, got[key], value)
		}
	}
}

func TestUniFiExportConvertsIPv4ToCIDR(t *testing.T) {
	req := exportRequest{Data: "ipv4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "example",
		Group: "test",
		IP4:   []string{"203.0.113.10"},
		CIDR4: []string{"198.51.100.0/24"},
	}})

	want := map[string]bool{"203.0.113.10/32": false, "198.51.100.0/24": false}
	for _, value := range values {
		if _, ok := want[value]; ok {
			want[value] = true
		}
	}
	for value, found := range want {
		if !found {
			t.Fatalf("missing %s in %v", value, values)
		}
	}
}

func TestUniFiDomainExportDropsSubdomainsCoveredByParent(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:    "example",
		Group:   "test",
		Domains: []string{"api.vndb.org", "notvndb.org", "vndb.org", "www.vndb.org"},
	}})

	want := map[string]bool{"notvndb.org": false, "vndb.org": false}
	if len(values) != len(want) {
		t.Fatalf("domains = %v, want only parent-covered compact list", values)
	}
	for _, value := range values {
		if _, ok := want[value]; !ok {
			t.Fatalf("unexpected domain %s in %v", value, values)
		}
		want[value] = true
	}
	for value, found := range want {
		if !found {
			t.Fatalf("missing %s in %v", value, values)
		}
	}
}

func TestTextDomainExportKeepsSubdomains(t *testing.T) {
	req := exportRequest{Format: "text", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:    "example",
		Group:   "test",
		Domains: []string{"vndb.org", "www.vndb.org"},
	}})

	if len(values) != 2 {
		t.Fatalf("text domain export should keep explicit subdomains: %v", values)
	}
}

func TestExportDeduplicatesValuesAcrossServices(t *testing.T) {
	req := exportRequest{Format: "text", Data: "ipv4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{
		{
			Name:  "one",
			Group: "test",
			IP4:   []string{"203.0.113.10"},
			CIDR4: []string{"198.51.100.0/24"},
		},
		{
			Name:  "two",
			Group: "test",
			IP4:   []string{"203.0.113.10"},
			CIDR4: []string{"198.51.100.0/24"},
		},
	})

	want := []string{"198.51.100.0/24", "203.0.113.10/32"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("deduplicated values = %v, want %v", values, want)
	}
}

func TestTextExportBodyExplainsEmptyResults(t *testing.T) {
	body := joinedLines(nil)
	if string(body) != "# no entries for requested export\n" {
		t.Fatalf("empty text export body = %q", body)
	}
}

func TestIPv4ImportSkipsAddressesCoveredByCIDR(t *testing.T) {
	req := exportRequest{Format: "text", Data: "ipv4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "example",
		Group: "test",
		IP4:   []string{"198.51.100.5", "203.0.113.10"},
		CIDR4: []string{"203.0.113.0/24"},
	}})

	want := []string{"198.51.100.5/32", "203.0.113.0/24"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("ipv4 import values = %v, want %v", values, want)
	}
}

func TestIPv6ImportSkipsAddressesCoveredByCIDR(t *testing.T) {
	req := exportRequest{Format: "text", Data: "ipv6", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "example",
		Group: "test",
		IP6:   []string{"2001:db8::10", "2001:db8:1::10"},
		CIDR6: []string{"2001:db8::/64"},
	}})

	want := []string{"2001:db8:1::10/128", "2001:db8::/64"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("ipv6 import values = %v, want %v", values, want)
	}
}

func TestUniFiIPv4OptimizesPolicyBasedRoutingNetworks(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "ipv4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name: "example",
		IP4: []string{
			"8.8.8.1",
			"8.8.9.10",
			"10.0.0.5",
		},
		CIDR4: []string{
			"8.8.8.16/28",
			"8.8.8.0/25",
			"8.8.8.128/25",
			"8.8.9.10/24",
			"10.0.0.0/8",
		},
	}})

	want := []string{"8.8.8.0/23"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("optimized IPv4 values = %v, want %v", values, want)
	}
}

func TestUniFiIPv4DoesNotMergeWhenSiblingIsMissing(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "cidr4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "example",
		CIDR4: []string{"8.8.8.0/25", "8.8.9.0/24"},
	}})

	want := []string{"8.8.8.0/25", "8.8.9.0/24"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("optimized IPv4 values = %v, want %v", values, want)
	}
}

func TestUniFiIPv6OptimizesPolicyBasedRoutingNetworks(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "ipv6", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name: "example",
		IP6: []string{
			"2001:4860:4860::8888",
			"2001:4860:4860::8889",
			"2001:db8::1",
		},
		CIDR6: []string{"2001:db8::/32"},
	}})

	want := []string{"2001:4860:4860::8888/127"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("optimized IPv6 values = %v, want %v", values, want)
	}
}

func TestTextCIDRExportDoesNotApplyUniFiNetworkOptimizations(t *testing.T) {
	req := exportRequest{Format: "text", Data: "cidr4", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "example",
		CIDR4: []string{"8.8.8.0/25", "8.8.8.128/25", "10.0.0.0/8"},
	}})

	want := []string{"10.0.0.0/8", "8.8.8.0/25", "8.8.8.128/25"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("text CIDR values = %v, want %v", values, want)
	}
}

func TestUniFiDomainExportDeduplicatesNormalizedDomains(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:    "example",
		Group:   "test",
		Domains: []string{"*.example.com", "EXAMPLE.com", "api.example.com", "example.com"},
	}})

	if len(values) != 1 || domainKey(values[0]) != "example.com" {
		t.Fatalf("normalized UniFi domains = %v, want one example.com entry", values)
	}
}

func TestUniFiDomainExportRollsUpDenseParentDomains(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:  "ubiquiti",
		Group: "shop",
		Domains: []string{
			"account.ui.com",
			"api.ui.com",
			"assets.ui.com",
			"blog.ui.com",
			"cloud.ui.com",
			"downloads.ui.com",
			"shop.ui.com",
			"www.ui.com",
			"account.ubnt.com",
			"airmax.ubnt.com",
			"blog.ubnt.com",
			"community.ubnt.com",
			"download.ubnt.com",
			"fw-update.ubnt.com",
			"store.ubnt.com",
			"www.ubnt.com",
		},
	}})

	want := []string{"ubnt.com", "ui.com"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("rolled up domains = %v, want %v", values, want)
	}
}

func TestUniFiDomainExportDoesNotRollUpSparseParentDomains(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:    "example",
		Group:   "test",
		Domains: []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com", "g.example.com"},
	}})

	if len(values) != 7 {
		t.Fatalf("sparse domains were rolled up unexpectedly: %v", values)
	}
	for _, value := range values {
		if value == "example.com" {
			t.Fatalf("sparse domains should not contain synthetic parent: %v", values)
		}
	}
}

func TestUniFiDomainExportDoesNotRollUpWithCoverageExcludes(t *testing.T) {
	req := exportRequest{
		Format: "unifi",
		Data:   "domains",
		Excl: map[string]map[string]struct{}{
			"domain": {"blocked.example.com": {}},
			"site":   {},
			"group":  {},
		},
	}
	values := req.exportValues([]Site{{
		Name: "example",
		Domains: []string{
			"a.example.com",
			"b.example.com",
			"c.example.com",
			"d.example.com",
			"e.example.com",
			"f.example.com",
			"g.example.com",
			"h.example.com",
			"blocked.example.com",
		},
	}})

	if len(values) != 8 {
		t.Fatalf("excluded-domain export = %v, want eight remaining child domains", values)
	}
	for _, value := range values {
		if value == "example.com" || value == "blocked.example.com" {
			t.Fatalf("excluded-domain export leaked %s in %v", value, values)
		}
	}
}

func TestUniFiDomainExportDoesNotRollUpKnownPublicSuffix(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name: "uk",
		Domains: []string{
			"a.one.co.uk",
			"b.two.co.uk",
			"c.three.co.uk",
			"d.four.co.uk",
			"e.five.co.uk",
			"f.six.co.uk",
			"g.seven.co.uk",
			"h.eight.co.uk",
		},
	}})

	if len(values) != 8 {
		t.Fatalf("public-suffix domains were rolled up unexpectedly: %v", values)
	}
	for _, value := range values {
		if value == "co.uk" {
			t.Fatalf("public suffix leaked into export: %v", values)
		}
	}
}

func TestUniFiDomainExportDropsInvalidDomains(t *testing.T) {
	req := exportRequest{Format: "unifi", Data: "domains", Excl: map[string]map[string]struct{}{}}
	values := req.exportValues([]Site{{
		Name:    "example",
		Domains: []string{"example.com", "mailto", "bad domain.example.com", "broken/entry.example.com", "xn--e1afmkfd.xn--p1ai"},
	}})

	want := []string{"example.com", "xn--e1afmkfd.xn--p1ai"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("valid UniFi domains = %v, want %v", values, want)
	}
}

func TestJSONSiteDataMapDeduplicatesPerSiteValues(t *testing.T) {
	req := exportRequest{Format: "json", Data: "ipv4", Excl: map[string]map[string]struct{}{}}
	values := siteDataMap(req, []Site{{
		Name:  "example",
		Group: "test",
		IP4:   []string{"203.0.113.10", "203.0.113.10"},
		CIDR4: []string{"203.0.113.10/32"},
	}})

	got := values["example"]
	if len(got) != 1 || got[0] != "203.0.113.10/32" {
		t.Fatalf("json per-site values = %v, want one 203.0.113.10/32", got)
	}
}

func TestDnsmasqIPSetFormat(t *testing.T) {
	req := exportRequest{Format: "ipset", Data: "domains", Excl: map[string]map[string]struct{}{}}
	lines := ipsetLines(req, []Site{{
		Name:    "example",
		Domains: []string{"vndb.org", "www.vndb.org"},
	}})

	want := []string{"ipset=/vndb.org/vpn_domains"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("ipset lines = %v, want %v", lines, want)
	}
}

func TestDnsmasqNFSetFormatUsesIPFamily(t *testing.T) {
	req := exportRequest{Format: "nfset", Data: "cidr6", Excl: map[string]map[string]struct{}{}}
	lines := nfsetLines(req, []Site{{
		Name:  "example",
		CIDR6: []string{"2001:4860::/32"},
	}})

	want := []string{"nftset=/2001:4860::/32/6#inet#fw4#vpn_cidr6"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("nfset lines = %v, want %v", lines, want)
	}
}

func TestAmneziaFormat(t *testing.T) {
	req := exportRequest{Format: "amnezia", Data: "domains", Excl: map[string]map[string]struct{}{}}
	entries := amneziaEntries(req, []Site{{
		Name:    "example",
		Domains: []string{"vndb.org", "www.vndb.org"},
	}})

	if len(entries) != 2 || entries[0].Hostname != "vndb.org" || entries[0].IP != "" {
		t.Fatalf("amnezia entries = %#v", entries)
	}
}

func TestExportCacheServesFullExportsAndRefreshes(t *testing.T) {
	data := &AllData{
		Sets: map[string]*ConfigSetData{
			"main": {
				ConfigSet: "main",
				Sites: []Site{{
					Name:  "example",
					Group: "test",
					IP4:   []string{"8.8.8.8"},
				}},
			},
		},
	}
	cfg := Config{DataRoot: t.TempDir(), ExportCacheDir: "cache"}
	cache := NewExportCache(cfg, data, slog.New(slog.NewTextHandler(io.Discard, nil)))
	data.ExportCache = cache

	if err := cache.RefreshSet("main"); err != nil {
		t.Fatal(err)
	}

	req := exportRequest{Format: "unifi", Data: "ipv4", Excl: emptyExportExcludes()}
	cached, ok := cache.Get("main", req)
	if !ok {
		t.Fatal("expected cache hit for full export")
	}
	body, err := os.ReadFile(cached.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "8.8.8.8/32" {
		t.Fatalf("cached body = %q, want 8.8.8.8/32", body)
	}

	path := filepath.Join(cfg.DataRoot, "cache", "main", "unifi", "ipv4.txt")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(body) {
		t.Fatalf("cached file = %q, want %q", content, body)
	}

	data.Sets["main"].Sites[0].IP4 = append(data.Sets["main"].Sites[0].IP4, "8.8.4.4")
	if err := cache.RefreshSet("main"); err != nil {
		t.Fatal(err)
	}
	cached, ok = cache.Get("main", req)
	if !ok {
		t.Fatal("expected cache hit after refresh")
	}
	body, err = os.ReadFile(cached.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "8.8.4.4/32\n8.8.8.8/32" {
		t.Fatalf("refreshed cached body = %q", body)
	}

	filtered := req
	filtered.Sites = map[string]struct{}{"example": {}}
	if _, ok := cache.Get("main", filtered); ok {
		t.Fatal("filtered export should bypass cache")
	}
}

func TestDNSRuntimeStatusPersistsAcrossRestart(t *testing.T) {
	cfg := Config{DataRoot: t.TempDir(), DNSRuntimeDir: "runtime/dns", DNSRefreshEnabled: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewDNSRuntimeStore(cfg, logger)

	store.SetStatus("main", func(status DNSRefreshStatus) DNSRefreshStatus {
		status.Running = false
		status.Processed = 216
		status.Total = 216
		status.Resolved = 446
		status.Added = 42
		status.LastStartedAt = "2026-05-27T03:00:00Z"
		status.LastFinishedAt = "2026-05-27T03:04:46Z"
		status.NextRunAt = "2026-05-28T03:00:00Z"
		return status
	})

	restarted := NewDNSRuntimeStore(cfg, logger)
	if err := restarted.Load(&AllData{Sets: map[string]*ConfigSetData{"main": {ConfigSet: "main"}}}); err != nil {
		t.Fatal(err)
	}
	got := restarted.Status("main")
	if !got.Enabled || got.Running {
		t.Fatalf("status flags after restart = %#v", got)
	}
	if got.Processed != 216 || got.Total != 216 || got.Resolved != 446 || got.Added != 42 {
		t.Fatalf("status counters after restart = %#v", got)
	}
	if got.LastStartedAt != "2026-05-27T03:00:00Z" || got.LastFinishedAt != "2026-05-27T03:04:46Z" || got.NextRunAt != "2026-05-28T03:00:00Z" {
		t.Fatalf("status timestamps after restart = %#v", got)
	}
}

func TestDNSRuntimeStatusClearsRunningStateAfterRestart(t *testing.T) {
	cfg := Config{DataRoot: t.TempDir(), DNSRuntimeDir: "runtime/dns", DNSRefreshEnabled: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewDNSRuntimeStore(cfg, logger)

	store.SetStatus("main", func(status DNSRefreshStatus) DNSRefreshStatus {
		status.Running = true
		status.CurrentSite = "example.com"
		status.Processed = 12
		status.Total = 216
		status.LastStartedAt = "2026-05-27T03:00:00Z"
		return status
	})

	restarted := NewDNSRuntimeStore(cfg, logger)
	if err := restarted.Load(&AllData{Sets: map[string]*ConfigSetData{"main": {ConfigSet: "main"}}}); err != nil {
		t.Fatal(err)
	}
	got := restarted.Status("main")
	if got.Running || got.CurrentSite != "" {
		t.Fatalf("running state should be cleared after restart: %#v", got)
	}
	if got.Processed != 12 || got.Total != 216 || got.LastStartedAt != "2026-05-27T03:00:00Z" {
		t.Fatalf("progress should remain available after restart: %#v", got)
	}
}

func TestDNSRuntimeStatusDerivesFromExistingAdditions(t *testing.T) {
	cfg := Config{DataRoot: t.TempDir(), DNSRuntimeDir: "runtime/dns", DNSRefreshEnabled: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewDNSRuntimeStore(cfg, logger)

	site := Site{Name: "example.com", Group: "test"}
	if err := store.SetAdditions("main", site, DNSRuntimeAdditions{IP4: []string{"203.0.113.10"}, IP6: []string{"2001:db8::10"}}); err != nil {
		t.Fatal(err)
	}

	restarted := NewDNSRuntimeStore(cfg, logger)
	if err := restarted.Load(&AllData{Sets: map[string]*ConfigSetData{
		"main": {
			ConfigSet: "main",
			Sites: []Site{
				site,
				{Name: "other.example", Group: "test"},
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	got := restarted.Status("main")
	if got.Processed != 1 || got.Total != 2 || got.Added != 2 {
		t.Fatalf("derived status = %#v, want processed=1 total=2 added=2", got)
	}
	if got.LastStartedAt == "" || got.LastFinishedAt == "" {
		t.Fatalf("derived status should include timestamps: %#v", got)
	}
}

func TestMikroTikScriptFormat(t *testing.T) {
	req := exportRequest{Format: "mikrotik", Data: "cidr4", Excl: map[string]map[string]struct{}{}}
	script := mikrotikScript(req, []Site{
		{Name: "one.example", Group: "media", CIDR4: []string{"8.8.8.0/25"}},
		{Name: "two.example", Group: "media", CIDR4: []string{"8.8.8.128/25"}},
	})

	for _, value := range []string{
		`/ip firewall address-list remove [find list="media_cidr4"];`,
		`add list="media_cidr4" address=8.8.8.0/25 comment="one.example";`,
		`add list="media_cidr4" address=8.8.8.128/25 comment="two.example";`,
	} {
		if !strings.Contains(script, value) {
			t.Fatalf("missing %q in MikroTik script:\n%s", value, script)
		}
	}
}

func TestCIDRExportAppliesConfigReplacements(t *testing.T) {
	data, err := LoadData(Config{ConfigSet: "main", DataRoot: fixtureDataRoot(t)})
	if err != nil {
		t.Fatal(err)
	}

	req := exportRequest{
		Data:  "cidr4",
		Sites: map[string]struct{}{"aistudio.google.com": {}},
		Excl:  map[string]map[string]struct{}{"cidr4": {}, "site": {}, "group": {}},
	}
	values := req.exportValues(req.selectSites(data.Sites))
	got := map[string]bool{}
	for _, value := range values {
		got[value] = true
	}

	if got["142.250.0.0/15"] {
		t.Fatalf("raw broad CIDR leaked into export: %v", values)
	}
	for _, value := range []string{"142.250.0.0/16", "142.251.0.0/16", "108.177.14.95/32"} {
		if !got[value] {
			t.Fatalf("missing replacement %s in %v", value, values)
		}
	}
	if len(values) != 3 {
		t.Fatalf("replacement count = %d, want 3: %v", len(values), values)
	}
}

func TestCIDRExportDropsEmptyConfigReplacements(t *testing.T) {
	data, err := LoadData(Config{ConfigSet: "main", DataRoot: fixtureDataRoot(t)})
	if err != nil {
		t.Fatal(err)
	}

	req := exportRequest{
		Data:  "cidr4",
		Sites: map[string]struct{}{"autodesk.com": {}},
		Excl:  map[string]map[string]struct{}{"cidr4": {}, "site": {}, "group": {}},
	}
	values := req.exportValues(req.selectSites(data.Sites))
	got := map[string]bool{}
	for _, value := range values {
		got[value] = true
	}

	for _, value := range []string{"1.0.0.0/9", "100.64.0.0/10", "104.16.0.0/12"} {
		if got[value] {
			t.Fatalf("CIDR %s should have been removed by empty replacement: %v", value, values)
		}
	}
	if !got["18.206.202.48/32"] {
		t.Fatalf("non-empty replacement is missing: %v", values)
	}
}

func TestExportSelectSitesUsesUnionOfSitesAndGroups(t *testing.T) {
	req := exportRequest{
		Sites:  map[string]struct{}{"single": {}},
		Groups: map[string]struct{}{"media": {}},
		Excl:   map[string]map[string]struct{}{"site": {}, "group": {}},
	}
	sites := req.selectSites([]Site{
		{Name: "video", Group: "media"},
		{Name: "music", Group: "media"},
		{Name: "single", Group: "tools"},
		{Name: "other", Group: "tools"},
	})

	got := map[string]bool{}
	for _, site := range sites {
		got[site.Name] = true
	}
	for _, name := range []string{"video", "music", "single"} {
		if !got[name] {
			t.Fatalf("expected %s in selected sites: %#v", name, got)
		}
	}
	if got["other"] {
		t.Fatalf("unexpected other site in selected sites: %#v", got)
	}
}

func fixtureDataRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureSite(t, root, "master", "ai", "aistudio.google.com", siteConfig{
		Domains: []string{"aistudio.google.com"},
		CIDR4:   []string{"142.250.0.0/15"},
		Replace: replaceConfig{CIDR4: map[string][]string{
			"142.250.0.0/15": {"142.250.0.0/16", "142.251.0.0/16", "108.177.14.95/32"},
		}},
	})
	writeFixtureSite(t, root, "master", "tools", "autodesk.com", siteConfig{
		Domains: []string{"autodesk.com"},
		CIDR4:   []string{"1.0.0.0/9", "100.64.0.0/10", "104.16.0.0/12", "18.206.202.48/32"},
		Replace: replaceConfig{CIDR4: map[string][]string{
			"1.0.0.0/9":        {},
			"100.64.0.0/10":    {},
			"104.16.0.0/12":    {},
			"18.206.202.48/32": {"18.206.202.48/32"},
		}},
	})
	writeFixtureSite(t, root, "beta", "test", "beta.example", siteConfig{
		Domains: []string{"beta.example"},
	})
	writeFixtureSite(t, root, "alpha", "test", "alpha.example", siteConfig{
		Domains: []string{"alpha.example"},
	})
	writeFixtureSite(t, root, "russia", "test", "russia.example", siteConfig{
		Domains: []string{"russia.example"},
	})

	storageDir := filepath.Join(root, "storage", "icons")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "generic.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	icons := map[string]string{
		"aistudio.google.com": "generic.svg",
		"autodesk.com":        "generic.svg",
		"alpha.example":       "generic.svg",
		"beta.example":        "generic.svg",
		"russia.example":      "generic.svg",
	}
	content, err := json.MarshalIndent(icons, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "storage", "icons.json"), append(content, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFixtureSite(t *testing.T, root string, configSet string, group string, name string, cfg siteConfig) {
	t.Helper()
	path := filepath.Join(root, "config", configSet, group, name+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExportSelectSitesSupportsExcludesWithoutIncludes(t *testing.T) {
	req := exportRequest{
		Excl: map[string]map[string]struct{}{
			"site":  {"blocked": {}},
			"group": {"hidden": {}},
		},
	}
	sites := req.selectSites([]Site{
		{Name: "kept", Group: "visible"},
		{Name: "blocked", Group: "visible"},
		{Name: "hidden-one", Group: "hidden"},
	})

	if len(sites) != 1 || sites[0].Name != "kept" {
		t.Fatalf("selected sites = %#v, want only kept", sites)
	}
}

func TestMergedSitesAddsDNSRuntimeOnlyWhenMissing(t *testing.T) {
	data := &AllData{
		Sets: map[string]*ConfigSetData{
			"main": {
				ConfigSet: "main",
				Sites: []Site{{
					Name:  "example",
					Group: "test",
					IP4:   []string{"203.0.113.10"},
					IP6:   []string{"2001:db8::10"},
					CIDR4: []string{"198.51.100.0/24"},
					CIDR6: []string{"2001:db8:1::/48"},
				}},
			},
		},
		Runtime: &DNSRuntimeStore{
			additions: map[string]map[string]DNSRuntimeAdditions{
				"main": {
					"example": {
						IP4: []string{"203.0.113.10", "198.51.100.25", "192.0.2.5"},
						IP6: []string{"2001:db8::10", "2001:db8:1::5", "2001:db8:2::5"},
					},
				},
			},
		},
	}

	sites := data.MergedSites("main")
	if len(sites) != 1 {
		t.Fatalf("expected one site, got %d", len(sites))
	}
	site := sites[0]
	if site.DynamicTotal != 2 {
		t.Fatalf("dynamic total = %d, want 2", site.DynamicTotal)
	}
	if len(site.IP4) != 2 || len(site.IP6) != 2 {
		t.Fatalf("merged IP lengths = (%d, %d), want (2, 2)", len(site.IP4), len(site.IP6))
	}
	ip4 := map[string]bool{}
	for _, value := range site.IP4 {
		ip4[value] = true
	}
	ip6 := map[string]bool{}
	for _, value := range site.IP6 {
		ip6[value] = true
	}
	if !ip4["192.0.2.5"] || !ip6["2001:db8:2::5"] {
		t.Fatalf("unexpected merged IPs: %#v %#v", site.IP4, site.IP6)
	}
}

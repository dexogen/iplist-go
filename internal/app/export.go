package app

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
)

type exportRequest struct {
	Format string
	Data   string
	Sites  map[string]struct{}
	Groups map[string]struct{}
	Excl   map[string]map[string]struct{}
}

type exportOptions struct {
	CompactDomains   bool
	RollupDomains    bool
	OptimizeNetworks bool
}

type amneziaEntry struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
}

func newExportRequest(r *http.Request) exportRequest {
	query := r.URL.Query()
	req := exportRequest{
		Format: strings.ToLower(first(query["format"], "json")),
		Data:   strings.ToLower(first(query["data"], "ipv4")),
		Sites:  set(query["site"]),
		Groups: set(query["group"]),
		Excl: map[string]map[string]struct{}{
			"site":   set(query["exclude[site]"]),
			"group":  set(query["exclude[group]"]),
			"domain": set(query["exclude[domain]"]),
			"ip4":    set(query["exclude[ip4]"]),
			"ip6":    set(query["exclude[ip6]"]),
			"cidr4":  set(query["exclude[cidr4]"]),
			"cidr6":  set(query["exclude[cidr6]"]),
		},
	}
	if req.Format == "" {
		req.Format = "json"
	}
	if req.Data == "" {
		req.Data = "ipv4"
	}
	return req
}

func (req exportRequest) selectSites(all []Site) []Site {
	var selected []Site
	hasIncludes := len(req.Sites) > 0 || len(req.Groups) > 0
	for _, site := range all {
		if hasIncludes && !contains(req.Sites, site.Name) && !contains(req.Groups, site.Group) {
			continue
		}
		if contains(req.Excl["site"], site.Name) || contains(req.Excl["group"], site.Group) {
			continue
		}
		selected = append(selected, site)
	}
	return selected
}

func (req exportRequest) exportValues(sites []Site) []string {
	return req.values(sites, exportOptions{
		CompactDomains:   req.Format == "unifi",
		RollupDomains:    req.Format == "unifi" && !req.hasDomainCoverageExcludes(),
		OptimizeNetworks: req.Format == "unifi",
	})
}

func (req exportRequest) values(sites []Site, options exportOptions) []string {
	var values []string
	for _, site := range sites {
		values = append(values, req.siteValues(site)...)
	}
	values = uniq(values)
	if options.CompactDomains && isDomainData(req.Data) {
		values = compactUniFiDomains(values, options.RollupDomains)
	}
	if options.OptimizeNetworks && isIPv4NetworkData(req.Data) {
		values = optimizeUniFiNetworks(values, 4)
	}
	if options.OptimizeNetworks && isIPv6NetworkData(req.Data) {
		values = optimizeUniFiNetworks(values, 6)
	}
	return values
}

func (req exportRequest) hasDomainCoverageExcludes() bool {
	return len(req.Excl["site"]) > 0 || len(req.Excl["group"]) > 0 || len(req.Excl["domain"]) > 0
}

func (req exportRequest) isFullExport() bool {
	if len(req.Sites) > 0 || len(req.Groups) > 0 {
		return false
	}
	for _, excluded := range req.Excl {
		if len(excluded) > 0 {
			return false
		}
	}
	return true
}

func (req exportRequest) siteValues(site Site) []string {
	switch req.Data {
	case "domains", "domain":
		return subtract(site.Domains, req.Excl["domain"])
	case "ip4":
		return subtract(site.IP4, req.Excl["ip4"])
	case "ip6":
		return subtract(site.IP6, req.Excl["ip6"])
	case "cidr4":
		return subtract(replaceCIDRs(site.CIDR4, site.Replace.CIDR4), req.Excl["cidr4"])
	case "cidr6":
		return subtract(replaceCIDRs(site.CIDR6, site.Replace.CIDR6), req.Excl["cidr6"])
	case "ipv6":
		cidr6 := replaceCIDRs(site.CIDR6, site.Replace.CIDR6)
		cidr6 = subtract(cidr6, req.Excl["cidr6"])
		values := make([]string, 0, len(site.IP6)+len(cidr6))
		for _, ip := range subtract(site.IP6, req.Excl["ip6"]) {
			if ipCoveredByCIDRs(ip, cidr6) {
				continue
			}
			values = append(values, ip+"/128")
		}
		values = append(values, cidr6...)
		return values
	case "ipv4", "":
		fallthrough
	default:
		cidr4 := replaceCIDRs(site.CIDR4, site.Replace.CIDR4)
		cidr4 = subtract(cidr4, req.Excl["cidr4"])
		values := make([]string, 0, len(site.IP4)+len(cidr4))
		for _, ip := range subtract(site.IP4, req.Excl["ip4"]) {
			if ipCoveredByCIDRs(ip, cidr4) {
				continue
			}
			values = append(values, ip+"/32")
		}
		values = append(values, cidr4...)
		return values
	}
}

func siteDataMap(req exportRequest, sites []Site) map[string][]string {
	output := map[string][]string{}
	for _, site := range sites {
		output[site.Name] = uniq(req.siteValues(site))
	}
	return output
}

func ipsetLines(req exportRequest, sites []Site) []string {
	values := req.values(sites, exportOptions{CompactDomains: true, OptimizeNetworks: true})
	setName := dnsmasqSetName(req.Data)
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, "ipset=/"+value+"/"+setName)
	}
	return lines
}

func nfsetLines(req exportRequest, sites []Site) []string {
	values := req.values(sites, exportOptions{CompactDomains: true, OptimizeNetworks: true})
	setName := dnsmasqSetName(req.Data)
	family := "4"
	if isIPv6NetworkData(req.Data) {
		family = "6"
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, "nftset=/"+value+"/"+family+"#inet#fw4#"+setName)
	}
	return lines
}

func amneziaEntries(req exportRequest, sites []Site) []amneziaEntry {
	values := req.values(sites, exportOptions{OptimizeNetworks: true})
	entries := make([]amneziaEntry, 0, len(values))
	for _, value := range values {
		entries = append(entries, amneziaEntry{Hostname: value, IP: ""})
	}
	return entries
}

func mikrotikScript(req exportRequest, sites []Site) string {
	type entry struct {
		Value    string
		Comments map[string]struct{}
	}

	lists := map[string]map[string]*entry{}
	for _, site := range sites {
		values := uniq(req.siteValues(site))
		if isIPv4NetworkData(req.Data) {
			values = optimizeUniFiNetworks(values, 4)
		}
		if isIPv6NetworkData(req.Data) {
			values = optimizeUniFiNetworks(values, 6)
		}

		listName := mikrotikListName(site.Group, req.Data)
		if lists[listName] == nil {
			lists[listName] = map[string]*entry{}
		}
		for _, value := range values {
			item := lists[listName][value]
			if item == nil {
				item = &entry{Value: value, Comments: map[string]struct{}{}}
				lists[listName][value] = item
			}
			item.Comments[site.Name] = struct{}{}
		}
	}

	listNames := make([]string, 0, len(lists))
	for name := range lists {
		listNames = append(listNames, name)
	}
	sort.Strings(listNames)

	var blocks []string
	for _, listName := range listNames {
		items := make([]*entry, 0, len(lists[listName]))
		for _, item := range lists[listName] {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Value < items[j].Value
		})

		var lines []string
		lines = append(lines, `/ip firewall address-list remove [find list="`+escapeRouterOS(listName)+`"];`)
		lines = append(lines, ":delay 5s")
		lines = append(lines, "")
		lines = append(lines, "/ip firewall address-list")
		for _, item := range items {
			lines = append(lines, `add list="`+escapeRouterOS(listName)+`" address=`+item.Value+` comment="`+escapeRouterOS(joinSet(item.Comments))+`";`)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

func dnsmasqSetName(data string) string {
	return "vpn_" + dataName(data)
}

func mikrotikListName(group string, data string) string {
	return sanitizeName(group) + "_" + dataName(data)
}

func dataName(data string) string {
	if data == "" {
		return "ipv4"
	}
	return data
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "default"
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if allowed {
			builder.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func escapeRouterOS(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func joinSet(values map[string]struct{}) string {
	output := make([]string, 0, len(values))
	for value := range values {
		output = append(output, value)
	}
	sort.Strings(output)
	return strings.Join(output, ",")
}

func first(values []string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	return values[0]
}

func set(values []string) map[string]struct{} {
	output := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			output[value] = struct{}{}
		}
	}
	return output
}

func contains(values map[string]struct{}, value string) bool {
	_, ok := values[value]
	return ok
}

func subtract(values []string, excluded map[string]struct{}) []string {
	if len(excluded) == 0 {
		return append([]string(nil), values...)
	}
	output := make([]string, 0, len(values))
	for _, value := range values {
		if !contains(excluded, value) {
			output = append(output, value)
		}
	}
	return output
}

func isDomainData(data string) bool {
	return data == "domains" || data == "domain"
}

func isIPv4NetworkData(data string) bool {
	return data == "cidr4" || data == "ipv4" || data == ""
}

func isIPv6NetworkData(data string) bool {
	return data == "cidr6" || data == "ipv6"
}

const uniFiDomainRollupThreshold = 8

func compactUniFiDomains(values []string, allowRollup bool) []string {
	domains := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain := domainKey(value)
		if domain == "" {
			continue
		}
		domains[domain] = struct{}{}
	}

	if allowRollup {
		for parent := range rollupParentDomains(domains, uniFiDomainRollupThreshold) {
			domains[parent] = struct{}{}
		}
	}

	keys := make([]string, 0, len(domains))
	for domain := range domains {
		if hasParentDomain(domain, domains) {
			continue
		}
		keys = append(keys, domain)
	}
	sort.Strings(keys)
	return keys
}

func domainKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, ".")
	value = strings.TrimPrefix(value, "*.")
	value = strings.TrimPrefix(value, ".")
	if value == "" || !strings.Contains(value, ".") || strings.ContainsAny(value, "/ :") {
		return ""
	}
	if !isValidDomainName(value) {
		return ""
	}
	return value
}

func isValidDomainName(domain string) bool {
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func rollupParentDomains(domains map[string]struct{}, threshold int) map[string]struct{} {
	if threshold < 2 {
		threshold = 2
	}
	counts := map[string]int{}
	for domain := range domains {
		labels := strings.Split(domain, ".")
		for index := 1; index < len(labels)-1; index++ {
			parent := strings.Join(labels[index:], ".")
			if !isCollapsibleDomainParent(parent) {
				continue
			}
			counts[parent]++
		}
	}

	output := map[string]struct{}{}
	for parent, count := range counts {
		if count >= threshold {
			output[parent] = struct{}{}
		}
	}
	return output
}

func isCollapsibleDomainParent(domain string) bool {
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	if len(labels) == 2 && isKnownTwoLabelPublicSuffix(labels[0], labels[1]) {
		return false
	}
	return true
}

func isKnownTwoLabelPublicSuffix(left string, tld string) bool {
	if len(tld) != 2 {
		return false
	}
	switch left {
	case "ac", "co", "com", "edu", "gov", "gouv", "mil", "ne", "net", "nom", "or", "org":
		return true
	default:
		return false
	}
}

func hasParentDomain(domain string, domains map[string]struct{}) bool {
	for index := strings.IndexByte(domain, '.'); index >= 0; index = strings.IndexByte(domain, '.') {
		domain = domain[index+1:]
		if _, ok := domains[domain]; ok {
			return true
		}
	}
	return false
}

func optimizeUniFiNetworks(values []string, family int) []string {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		if family == 4 && !prefix.Addr().Is4() {
			continue
		}
		if family == 6 && !prefix.Addr().Is6() {
			continue
		}
		if isUnroutablePrefix(prefix) {
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	prefixes = uniqPrefixes(prefixes)
	prefixes = removeCoveredPrefixes(prefixes)
	prefixes = mergeAdjacentPrefixes(prefixes)
	prefixes = removeCoveredPrefixes(prefixes)
	sortPrefixes(prefixes)

	output := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		output = append(output, prefix.String())
	}
	return output
}

func uniqPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	seen := map[netip.Prefix]struct{}{}
	output := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		output = append(output, prefix)
	}
	return output
}

func removeCoveredPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	sortPrefixes(prefixes)
	output := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefixCoveredByAny(prefix, output) {
			continue
		}
		output = append(output, prefix)
	}
	return output
}

func prefixCoveredByAny(prefix netip.Prefix, candidates []netip.Prefix) bool {
	for _, candidate := range candidates {
		if candidate.Addr().Is4() != prefix.Addr().Is4() {
			continue
		}
		if candidate.Bits() <= prefix.Bits() && candidate.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func mergeAdjacentPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	for {
		next, changed := mergeAdjacentPrefixesOnce(prefixes)
		prefixes = uniqPrefixes(next)
		if !changed {
			return prefixes
		}
	}
}

func mergeAdjacentPrefixesOnce(prefixes []netip.Prefix) ([]netip.Prefix, bool) {
	prefixes = uniqPrefixes(prefixes)
	byParent := map[string][]netip.Prefix{}
	for _, prefix := range prefixes {
		if prefix.Bits() == 0 {
			continue
		}
		parent := netip.PrefixFrom(prefix.Addr(), prefix.Bits()-1).Masked()
		key := parent.String()
		byParent[key] = append(byParent[key], prefix)
	}

	mergedParents := map[netip.Prefix]struct{}{}
	usedChildren := map[netip.Prefix]struct{}{}
	for _, children := range byParent {
		if len(children) != 2 {
			continue
		}
		first := children[0].Masked()
		second := children[1].Masked()
		if first == second || first.Bits() != second.Bits() || first.Addr().Is4() != second.Addr().Is4() {
			continue
		}
		parent := netip.PrefixFrom(first.Addr(), first.Bits()-1).Masked()
		mergedParents[parent] = struct{}{}
		usedChildren[first] = struct{}{}
		usedChildren[second] = struct{}{}
	}
	if len(mergedParents) == 0 {
		return prefixes, false
	}

	output := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		if _, ok := usedChildren[prefix.Masked()]; ok {
			continue
		}
		output = append(output, prefix)
	}
	for parent := range mergedParents {
		output = append(output, parent)
	}
	return output, true
}

func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool {
		left := prefixes[i].Masked()
		right := prefixes[j].Masked()
		if left.Addr().Is4() != right.Addr().Is4() {
			return left.Addr().Is4()
		}
		if compareAddr(left.Addr(), right.Addr()) != 0 {
			return compareAddr(left.Addr(), right.Addr()) < 0
		}
		return left.Bits() < right.Bits()
	})
}

func compareAddr(left netip.Addr, right netip.Addr) int {
	leftBytes := left.As16()
	rightBytes := right.As16()
	for i := range leftBytes {
		if leftBytes[i] < rightBytes[i] {
			return -1
		}
		if leftBytes[i] > rightBytes[i] {
			return 1
		}
	}
	return 0
}

var unroutablePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isUnroutablePrefix(prefix netip.Prefix) bool {
	prefix = prefix.Masked()
	for _, reserved := range unroutablePrefixes {
		if reserved.Addr().Is4() != prefix.Addr().Is4() {
			continue
		}
		if prefix.Bits() >= reserved.Bits() && reserved.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func replaceCIDRs(values []string, replacements map[string][]string) []string {
	if len(replacements) == 0 {
		return append([]string(nil), values...)
	}
	output := make([]string, 0, len(values))
	for _, value := range values {
		if replacement, ok := replacements[value]; ok {
			output = append(output, replacement...)
			continue
		}
		output = append(output, value)
	}
	return output
}

func ipCoveredByCIDRs(ip string, cidrs []string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func uniq(values []string) []string {
	seen := map[string]struct{}{}
	output := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	sort.Strings(output)
	return output
}

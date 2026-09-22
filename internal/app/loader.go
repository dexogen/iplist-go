package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func LoadAllData(cfg Config) (*AllData, error) {
	icons, err := loadIcons(filepath.Join(cfg.DataRoot, "storage", "icons.json"))
	if err != nil {
		return nil, err
	}
	sets, err := discoverConfigSets(cfg)
	if err != nil {
		return nil, err
	}

	all := &AllData{
		Sets:     map[string]*ConfigSetData{},
		SetInfos: sets,
		Icons:    icons,
	}
	for _, set := range sets {
		data, err := loadConfigSet(cfg, set, icons)
		if err != nil {
			return nil, err
		}
		all.Sets[set.Key] = data
		all.SetOrder = append(all.SetOrder, set.Key)
	}
	return all, nil
}

func LoadData(cfg Config) (*ConfigSetData, error) {
	icons, err := loadIcons(filepath.Join(cfg.DataRoot, "storage", "icons.json"))
	if err != nil {
		return nil, err
	}
	sets, err := discoverConfigSets(cfg)
	if err != nil {
		return nil, err
	}
	for _, set := range sets {
		if set.Key == cfg.ConfigSet {
			return loadConfigSet(cfg, set, icons)
		}
	}
	return nil, fmt.Errorf("missing config set %s", cfg.ConfigSet)
}

func loadConfigSet(cfg Config, set ConfigSetInfo, icons map[string]string) (*ConfigSetData, error) {
	configDir := filepath.Join(cfg.DataRoot, "config", set.Dir)

	var sites []Site
	err := filepath.WalkDir(configDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}

		rel, err := filepath.Rel(configDir, path)
		if err != nil {
			return err
		}
		group := filepath.Dir(rel)
		if group == "." {
			group = "default"
		}
		name := strings.TrimSuffix(filepath.Base(path), ".json")

		var raw siteConfig
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(content, &raw); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		icon := icons[name]
		if icon == "" {
			icon = "generic.svg"
		}

		replacements := normalizeReplace(raw.Replace)
		cidr4 := replaceCIDRs(normalizeStrings(raw.CIDR4), replacements.CIDR4)
		cidr6 := replaceCIDRs(normalizeStrings(raw.CIDR6), replacements.CIDR6)

		sites = append(sites, Site{
			Name:    name,
			Group:   filepath.ToSlash(group),
			Domains: normalizeStrings(raw.Domains),
			DNS:     normalizeStrings(raw.DNS),
			Timeout: raw.Timeout,
			IP4:     normalizeStrings(raw.IP4),
			IP6:     normalizeStrings(raw.IP6),
			CIDR4:   normalizeStrings(cidr4),
			CIDR6:   normalizeStrings(cidr6),
			Icon:    "/favicon?site=" + name,
			Replace: replacements,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Group == sites[j].Group {
			return sites[i].Name < sites[j].Name
		}
		return sites[i].Group < sites[j].Group
	})

	return &ConfigSetData{
		ConfigSet: set.Key,
		Sites:     sites,
		Groups:    buildGroups(sites),
	}, nil
}

func discoverConfigSets(cfg Config) ([]ConfigSetInfo, error) {
	configRoot := filepath.Join(cfg.DataRoot, "config")
	entries, err := os.ReadDir(configRoot)
	if err != nil {
		return nil, err
	}

	var sets []ConfigSetInfo
	seen := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := entry.Name()
		if strings.HasPrefix(dir, ".") {
			continue
		}
		key := configSetKeyFromDir(dir)
		if !validConfigSetKey(key) || reservedConfigSetKey(key) {
			return nil, fmt.Errorf("invalid config set directory %s", dir)
		}
		if previous, ok := seen[key]; ok {
			return nil, fmt.Errorf("config set directories %s and %s both map to %s", previous, dir, key)
		}
		seen[key] = dir
		sets = append(sets, ConfigSetInfo{
			Key:       key,
			Dir:       dir,
			Label:     configSetLabel(dir),
			URL:       configSetURL(key),
			APIBase:   configSetAPIBase(key),
			IsDefault: key == "main",
		})
	}
	sort.Slice(sets, func(i, j int) bool {
		leftRank := configSetSortRank(sets[i].Key)
		rightRank := configSetSortRank(sets[j].Key)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return sets[i].Key < sets[j].Key
	})
	if len(sets) == 0 {
		return nil, fmt.Errorf("no config sets found in %s", configRoot)
	}
	return sets, nil
}

func configSetKeyFromDir(dir string) string {
	if dir == "master" {
		return "main"
	}
	return dir
}

func configSetSortRank(key string) int {
	switch key {
	case "main":
		return 0
	case "beta":
		return 1
	case "russia":
		return 2
	default:
		return 100
	}
}

func configSetLabel(dir string) string {
	parts := strings.FieldsFunc(dir, func(char rune) bool {
		return char == '-' || char == '_' || char == '.'
	})
	if len(parts) == 0 {
		return dir
	}
	for index, part := range parts {
		if part == "" {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func configSetURL(key string) string {
	if key == "main" {
		return "/"
	}
	return "/" + key
}

func configSetAPIBase(key string) string {
	if key == "main" {
		return "/api/latest"
	}
	return "/api/" + key
}

func validConfigSetKey(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range key {
		ok := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.'
		if !ok {
			return false
		}
		if index == 0 && !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func reservedConfigSetKey(key string) bool {
	switch key {
	case "api", "assets", "favicon", "latest", "master":
		return true
	default:
		return false
	}
}

func (d *AllData) ConfigSetKeys() []string {
	if len(d.SetOrder) > 0 {
		return append([]string(nil), d.SetOrder...)
	}
	keys := make([]string, 0, len(d.Sets))
	for key := range d.Sets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func loadIcons(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	icons := map[string]string{}
	if err := json.Unmarshal(content, &icons); err != nil {
		return nil, err
	}
	return icons, nil
}

func buildGroups(sites []Site) []GroupSummary {
	byGroup := map[string][]SiteSummary{}
	for _, site := range sites {
		byGroup[site.Group] = append(byGroup[site.Group], SiteSummary{
			Name:  site.Name,
			Group: site.Group,
			Icon:  site.Icon,
			Counts: map[string]int{
				"domains": len(site.Domains),
				"ip4":     len(site.IP4),
				"ip6":     len(site.IP6),
				"cidr4":   len(site.CIDR4),
				"cidr6":   len(site.CIDR6),
			},
			DynamicCounts: site.DynamicCounts,
			DynamicTotal:  site.DynamicTotal,
		})
	}

	names := make([]string, 0, len(byGroup))
	for name := range byGroup {
		names = append(names, name)
	}
	sort.Strings(names)

	groups := make([]GroupSummary, 0, len(names))
	for _, name := range names {
		groups = append(groups, GroupSummary{Name: name, Sites: byGroup[name]})
	}
	return groups
}

func normalizeStrings(input []string) []string {
	seen := map[string]struct{}{}
	output := make([]string, 0, len(input))
	for _, item := range input {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		output = append(output, item)
	}
	sort.Strings(output)
	return output
}

func normalizeReplace(input replaceConfig) replaceConfig {
	return replaceConfig{
		CIDR4: normalizeReplaceMap(input.CIDR4),
		CIDR6: normalizeReplaceMap(input.CIDR6),
	}
}

func normalizeReplaceMap(input map[string][]string) map[string][]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string][]string, len(input))
	for key, values := range input {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		output[key] = normalizeStrings(values)
	}
	return output
}

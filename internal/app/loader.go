package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var configSets = []string{"main", "beta", "russia"}

func LoadAllData(cfg Config) (*AllData, error) {
	icons, err := loadIcons(filepath.Join(cfg.DataRoot, "storage", "icons.json"))
	if err != nil {
		return nil, err
	}

	all := &AllData{
		Sets:  map[string]*ConfigSetData{},
		Icons: icons,
	}
	for _, configSet := range configSets {
		data, err := loadConfigSet(cfg, configSet, icons)
		if err != nil {
			return nil, err
		}
		all.Sets[configSet] = data
	}
	return all, nil
}

func LoadData(cfg Config) (*ConfigSetData, error) {
	icons, err := loadIcons(filepath.Join(cfg.DataRoot, "storage", "icons.json"))
	if err != nil {
		return nil, err
	}
	return loadConfigSet(cfg, cfg.ConfigSet, icons)
}

func loadConfigSet(cfg Config, configSet string, icons map[string]string) (*ConfigSetData, error) {
	configDir := filepath.Join(cfg.DataRoot, "config", configDirName(configSet))

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
		ConfigSet: configSet,
		Sites:     sites,
		Groups:    buildGroups(sites),
	}, nil
}

func configDirName(configSet string) string {
	if configSet == "main" {
		return "master"
	}
	return configSet
}

func loadIcons(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
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

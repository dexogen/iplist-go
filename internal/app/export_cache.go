package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ExportCache struct {
	mu      sync.RWMutex
	root    string
	data    *AllData
	logger  *slog.Logger
	entries map[string]map[exportCacheKey]cachedExport
}

type exportCacheKey struct {
	Format string
	Data   string
}

type cachedExport struct {
	ContentType string
	Filename    string
	GeneratedAt time.Time
	Path        string
}

var cachedExportKeys = []exportCacheKey{
	{Format: "unifi", Data: "domains"},
	{Format: "unifi", Data: "ipv4"},
	{Format: "unifi", Data: "cidr4"},
	{Format: "unifi", Data: "ipv6"},
	{Format: "unifi", Data: "cidr6"},
}

func NewExportCache(cfg Config, data *AllData, logger *slog.Logger) *ExportCache {
	root := cfg.ExportCacheDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(cfg.DataRoot, root)
	}
	return &ExportCache{
		root:    root,
		data:    data,
		logger:  logger,
		entries: map[string]map[exportCacheKey]cachedExport{},
	}
}

func (c *ExportCache) RefreshAll() error {
	for _, configSet := range configSets {
		if err := c.RefreshSet(configSet); err != nil {
			return err
		}
	}
	return nil
}

func (c *ExportCache) RefreshSet(configSet string) error {
	started := time.Now()
	entries := map[exportCacheKey]cachedExport{}
	for _, key := range cachedExportKeys {
		entry, body, err := c.build(configSet, key)
		if err != nil {
			return err
		}
		path, err := c.writeFile(configSet, key, body)
		if err != nil {
			return err
		}
		entry.Path = path
		entries[key] = entry
	}

	c.mu.Lock()
	c.entries[configSet] = entries
	c.mu.Unlock()

	c.logger.Info("export cache refreshed", "configSet", configSet, "entries", len(entries), "elapsed", time.Since(started).String())
	return nil
}

func (c *ExportCache) Get(configSet string, req exportRequest) (cachedExport, bool) {
	if !req.isFullExport() {
		return cachedExport{}, false
	}
	key := exportCacheKey{Format: normalizeExportFormat(req.Format), Data: dataName(req.Data)}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[configSet][key]
	if !ok {
		return cachedExport{}, false
	}
	return entry, true
}

func (c *ExportCache) build(configSet string, key exportCacheKey) (cachedExport, []byte, error) {
	data := c.data.MergedConfigSet(configSet)
	if data == nil {
		return cachedExport{}, nil, fmt.Errorf("missing config set %s", configSet)
	}
	req := exportRequest{
		Format: normalizeExportFormat(key.Format),
		Data:   dataName(key.Data),
		Excl:   emptyExportExcludes(),
	}
	sites := req.selectSites(data.Sites)

	var body []byte
	contentType := "text/plain; charset=utf-8"
	filename := "iplist.txt"
	switch req.Format {
	case "text", "unifi":
		body = joinedLines(req.exportValues(sites))
	case "mikrotik":
		body = []byte(mikrotikScript(req, sites))
		filename = "iplist.rsc"
	case "ipset":
		body = joinedLines(ipsetLines(req, sites))
		filename = "iplist-ipset.conf"
	case "nfset":
		body = joinedLines(nfsetLines(req, sites))
		filename = "iplist-nfset.conf"
	case "amnezia":
		encoded, err := json.Marshal(amneziaEntries(req, sites))
		if err != nil {
			return cachedExport{}, nil, err
		}
		body = append(encoded, '\n')
		contentType = "application/json"
		filename = "iplist-amnezia.json"
	default:
		return cachedExport{}, nil, fmt.Errorf("unsupported cached export format %s", req.Format)
	}

	return cachedExport{
		ContentType: contentType,
		Filename:    filename,
		GeneratedAt: time.Now().UTC(),
	}, body, nil
}

func (c *ExportCache) writeFile(configSet string, key exportCacheKey, body []byte) (string, error) {
	dir := filepath.Join(c.root, configSet, key.Format)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, key.Data+exportCacheExtension(key.Format))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func exportCacheExtension(format string) string {
	switch format {
	case "amnezia":
		return ".json"
	case "mikrotik":
		return ".rsc"
	case "ipset", "nfset":
		return ".conf"
	default:
		return ".txt"
	}
}

func normalizeExportFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return "json"
	}
	return format
}

func emptyExportExcludes() map[string]map[string]struct{} {
	return map[string]map[string]struct{}{
		"site":   {},
		"group":  {},
		"domain": {},
		"ip4":    {},
		"ip6":    {},
		"cidr4":  {},
		"cidr6":  {},
	}
}

func joinedLines(values []string) []byte {
	return []byte(strings.Join(values, "\n"))
}

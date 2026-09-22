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
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxSnapshotBytes = 512 << 20

type SnapshotDescriptor struct {
	Path          string         `json:"path"`
	SHA256        string         `json:"sha256"`
	Bytes         int64          `json:"bytes"`
	UnpackedBytes int64          `json:"unpacked_bytes"`
	Sites         int            `json:"sites"`
	Counts        map[string]int `json:"counts"`
	Status        string         `json:"status"`
	LastSuccessAt time.Time      `json:"last_success_at"`
	CheckedAt     time.Time      `json:"checked_at"`
	Warnings      []string       `json:"warnings"`
}

type SnapshotManifest struct {
	SchemaVersion int                           `json:"schema_version"`
	PublishedAt   time.Time                     `json:"published_at"`
	Revision      string                        `json:"revision"`
	Sets          map[string]SnapshotDescriptor `json:"sets"`
}

type snapshotSite struct {
	siteConfig
	Name  string `json:"name"`
	Group string `json:"group"`
}

type snapshotPayload struct {
	SchemaVersion int            `json:"schema_version"`
	ConfigSet     string         `json:"config_set"`
	Sites         []snapshotSite `json:"sites"`
}

type SnapshotRefreshStatus struct {
	Enabled       bool      `json:"enabled"`
	Running       bool      `json:"running"`
	Revision      string    `json:"revision,omitempty"`
	LastCheckedAt time.Time `json:"lastCheckedAt,omitempty"`
	LastSuccessAt time.Time `json:"lastSuccessAt,omitempty"`
	NextRunAt     time.Time `json:"nextRunAt,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}

type snapshotClient struct {
	cfg    Config
	client *http.Client
	root   string
}

func newSnapshotClient(cfg Config) *snapshotClient {
	root := cfg.SnapshotDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(cfg.DataRoot, root)
	}
	return &snapshotClient{cfg: cfg, root: root, client: &http.Client{Timeout: cfg.SnapshotTimeout}}
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

func (c *snapshotClient) get(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot source returned %s", response.Status)
	}
	return readLimited(response.Body, limit)
}

func parseManifest(body []byte) (*SnapshotManifest, error) {
	var manifest SnapshotManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 || manifest.PublishedAt.IsZero() || len(manifest.Sets) == 0 || len(manifest.Sets) > 64 {
		return nil, fmt.Errorf("invalid snapshot manifest")
	}
	for _, key := range []string{"main", "beta", "russia"} {
		if _, ok := manifest.Sets[key]; !ok {
			return nil, fmt.Errorf("manifest is missing %s", key)
		}
	}
	for key, desc := range manifest.Sets {
		for _, field := range []string{"domains", "ip4", "ip6", "cidr4", "cidr6"} {
			if count, ok := desc.Counts[field]; !ok || count < 0 {
				return nil, fmt.Errorf("missing or invalid %s count for %s", field, key)
			}
		}
		digest, err := hex.DecodeString(desc.SHA256)
		if !validConfigSetKey(key) || reservedConfigSetKey(key) || err != nil || len(digest) != sha256.Size || desc.Path != "objects/"+desc.SHA256+".json.gz" || desc.Bytes <= 0 || desc.Bytes > maxSnapshotBytes || desc.UnpackedBytes <= 0 || desc.UnpackedBytes > maxSnapshotBytes || desc.Sites <= 0 || desc.LastSuccessAt.IsZero() || (desc.Status != "ok" && desc.Status != "degraded") {
			return nil, fmt.Errorf("invalid descriptor for %s", key)
		}
		if desc.LastSuccessAt.After(time.Now().Add(10 * time.Minute)) {
			return nil, fmt.Errorf("future source timestamp for %s", key)
		}
	}
	return &manifest, nil
}

func snapshotRevision(manifest *SnapshotManifest) string {
	keys := make([]string, 0, len(manifest.Sets))
	for key := range manifest.Sets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(h, "%s:%s\n", key, manifest.Sets[key].SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func snapshotObjectURL(base string, desc SnapshotDescriptor) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("snapshot URL must use HTTP(S)")
	}
	path := desc.Path
	if strings.Contains(parsed.Path, "/releases/download/") {
		path = filepath.Base(path)
	}
	relative, _ := url.Parse(path)
	return parsed.ResolveReference(relative).String(), nil
}

func validSnapshotName(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00\r\n\t ")
}

func validateSnapshotSite(raw snapshotSite) error {
	if !validSnapshotName(raw.Name) || !validSnapshotName(raw.Group) {
		return fmt.Errorf("invalid site identity")
	}
	if raw.Domains == nil || raw.IP4 == nil || raw.IP6 == nil || raw.CIDR4 == nil || raw.CIDR6 == nil {
		return fmt.Errorf("missing arrays for %s", raw.Name)
	}
	for _, domain := range raw.Domains {
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(domain, "*."), "."), ".")
		if host == "" || strings.ContainsAny(host, " /\\<>:@\r\n\t?*") {
			return fmt.Errorf("invalid domain %q", domain)
		}
	}
	for _, item := range []struct {
		values []string
		family int
		cidr   bool
	}{{raw.IP4, 4, false}, {raw.IP6, 6, false}, {raw.CIDR4, 4, true}, {raw.CIDR6, 6, true}} {
		for _, value := range item.values {
			var addr netip.Addr
			if item.cidr {
				prefix, err := netip.ParsePrefix(value)
				if err != nil {
					return fmt.Errorf("invalid CIDR %q", value)
				}
				addr = prefix.Addr()
			} else {
				var err error
				addr, err = netip.ParseAddr(value)
				if err != nil || addr.Zone() != "" {
					return fmt.Errorf("invalid IP %q", value)
				}
			}
			if addr.Is4() != (item.family == 4) {
				return fmt.Errorf("wrong address family: %s", value)
			}
		}
	}
	for family, mapping := range map[int]map[string][]string{4: raw.Replace.CIDR4, 6: raw.Replace.CIDR6} {
		for source, values := range mapping {
			for _, value := range append([]string{source}, values...) {
				prefix, err := netip.ParsePrefix(value)
				if err != nil || prefix.Addr().Is4() != (family == 4) {
					return fmt.Errorf("invalid replacement %q", value)
				}
			}
		}
	}
	return nil
}

func decodeSnapshot(body []byte, key string, desc SnapshotDescriptor) (*ConfigSetData, error) {
	digest := sha256.Sum256(body)
	if int64(len(body)) != desc.Bytes || hex.EncodeToString(digest[:]) != desc.SHA256 {
		return nil, fmt.Errorf("snapshot checksum/size mismatch for %s", key)
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	raw, err := readLimited(reader, desc.UnpackedBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != desc.UnpackedBytes {
		return nil, fmt.Errorf("expanded size mismatch for %s", key)
	}
	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.SchemaVersion != 1 || payload.ConfigSet != key || len(payload.Sites) != desc.Sites {
		return nil, fmt.Errorf("snapshot metadata mismatch for %s", key)
	}
	data := &ConfigSetData{ConfigSet: key}
	seen := map[string]bool{}
	counts := map[string]int{"domains": 0, "ip4": 0, "ip6": 0, "cidr4": 0, "cidr6": 0}
	for _, raw := range payload.Sites {
		if seen[raw.Name] {
			return nil, fmt.Errorf("duplicate site %s", raw.Name)
		}
		seen[raw.Name] = true
		if err := validateSnapshotSite(raw); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		counts["domains"] += len(raw.Domains)
		counts["ip4"] += len(raw.IP4)
		counts["ip6"] += len(raw.IP6)
		counts["cidr4"] += len(raw.CIDR4)
		counts["cidr6"] += len(raw.CIDR6)
		data.Sites = append(data.Sites, Site{Name: raw.Name, Group: raw.Group, Domains: raw.Domains, IP4: raw.IP4, IP6: raw.IP6, CIDR4: replaceCIDRs(raw.CIDR4, raw.Replace.CIDR4), CIDR6: replaceCIDRs(raw.CIDR6, raw.Replace.CIDR6), DNS: raw.DNS, Timeout: raw.Timeout, Replace: raw.Replace, Icon: "/favicon?site=" + url.QueryEscape(raw.Name)})
	}
	for field, count := range counts {
		if desc.Counts[field] != count {
			return nil, fmt.Errorf("%s/%s count mismatch", key, field)
		}
	}
	sort.Slice(data.Sites, func(i, j int) bool {
		if data.Sites[i].Group == data.Sites[j].Group {
			return data.Sites[i].Name < data.Sites[j].Name
		}
		return data.Sites[i].Group < data.Sites[j].Group
	})
	data.Groups = buildGroups(data.Sites)
	return data, nil
}

func (c *snapshotClient) load(ctx context.Context, manifest *SnapshotManifest, root string, download bool, previous *AllData) (*AllData, error) {
	data := &AllData{Sets: map[string]*ConfigSetData{}, Sources: manifest.Sets, Revision: snapshotRevision(manifest), Icons: map[string]string{}}
	icons, err := loadIcons(filepath.Join(c.cfg.DataRoot, "storage", "icons.json"))
	if err != nil {
		return nil, err
	}
	data.Icons = icons
	for key := range manifest.Sets {
		data.SetOrder = append(data.SetOrder, key)
	}
	sort.Slice(data.SetOrder, func(i, j int) bool {
		a, b := data.SetOrder[i], data.SetOrder[j]
		if configSetSortRank(a) == configSetSortRank(b) {
			return a < b
		}
		return configSetSortRank(a) < configSetSortRank(b)
	})
	for _, key := range data.SetOrder {
		desc := manifest.Sets[key]
		path := filepath.Join(root, filepath.FromSlash(desc.Path))
		_, cachedErr := os.Stat(path)
		if previous != nil && previous.Sources[key].SHA256 == desc.SHA256 && cachedErr == nil {
			data.Sets[key] = previous.Sets[key]
		} else {
			body, err := os.ReadFile(path)
			var set *ConfigSetData
			if err == nil {
				set, err = decodeSnapshot(body, key, desc)
			}
			if err != nil && download {
				address, urlErr := snapshotObjectURL(c.cfg.SnapshotURL, desc)
				if urlErr != nil {
					return nil, urlErr
				}
				body, err = c.get(ctx, address, desc.Bytes)
				if err != nil {
					return nil, err
				}
				set, err = decodeSnapshot(body, key, desc)
				if err == nil {
					err = atomicWrite(path, body)
				}
			}
			if err != nil {
				return nil, err
			}
			data.Sets[key] = set
		}
		dir := key
		if key == "main" {
			dir = "master"
		}
		data.SetInfos = append(data.SetInfos, ConfigSetInfo{Key: key, Dir: dir, Label: configSetLabel(dir), URL: configSetURL(key), APIBase: configSetAPIBase(key), IsDefault: key == "main"})
	}
	return data, nil
}

func atomicWrite(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

package app

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	urlpath "path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web/*
var embeddedWeb embed.FS

type Server struct {
	cfg    Config
	data   *AllData
	logger *slog.Logger
	web    fs.FS
}

func NewServer(cfg Config, data *AllData, logger *slog.Logger) http.Handler {
	webFS, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}
	return &Server{
		cfg:    cfg,
		data:   data,
		logger: logger,
		web:    webFS,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if configSet, prefix, path, ok := s.apiRoute(r.URL.Path); ok {
		data := s.data.Sets[configSet]
		if data == nil {
			http.NotFound(w, r)
			return
		}
		s.serveAPI(w, r, configSet, prefix, path)
		return
	}

	if strings.HasPrefix(urlpath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/api/") {
		http.NotFound(w, r)
		return
	}

	configSet, path := s.uiRoute(r.URL.Path)
	if strings.HasPrefix(path, "/api/") {
		http.NotFound(w, r)
		return
	}

	data := s.data.Sets[configSet]
	if data == nil {
		http.NotFound(w, r)
		return
	}

	s.serveWeb(w, path)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, configSet string, prefix string, path string) {
	if source, ok := s.data.Sources[configSet]; ok {
		status := source.Status
		if s.cfg.SnapshotMaxAge > 0 && time.Since(source.LastSuccessAt) > s.cfg.SnapshotMaxAge {
			status = "stale"
		}
		w.Header().Set("X-IPList-Snapshot", source.SHA256)
		w.Header().Set("X-IPList-Source-Status", status)
		w.Header().Set("X-IPList-Source-Updated-At", source.LastSuccessAt.UTC().Format(time.RFC3339))
	}
	switch {
	case path == "/runtime":
		s.runtime(w, configSet)
	case path == "/favicon":
		s.favicon(w, r)
	case path == "/" && r.URL.Query().Has("format"):
		if r.URL.Query().Get("format") == "json" && r.URL.Query().Get("data") == "runtime" {
			s.runtime(w, configSet)
			return
		}
		if s.serveCachedExport(w, r, configSet) {
			return
		}
		merged := s.data.MergedConfigSet(configSet)
		if merged == nil {
			http.NotFound(w, r)
			return
		}
		s.export(w, r, merged, prefix)
	case path == "/" || path == "/catalog":
		merged := s.data.MergedConfigSet(configSet)
		if merged == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, s.catalog(merged, prefix))
	case path == "/export":
		if s.serveCachedExport(w, r, configSet) {
			return
		}
		merged := s.data.MergedConfigSet(configSet)
		if merged == nil {
			http.NotFound(w, r)
			return
		}
		s.export(w, r, merged, prefix)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiRoute(requestPath string) (string, string, string, bool) {
	cleanPath := urlpath.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	if cleanPath == "/api" {
		return "", "", "", false
	}
	if !strings.HasPrefix(cleanPath, "/api/") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(cleanPath, "/api/")
	category, path, _ := strings.Cut(rest, "/")
	configSet := configSetForCategory(category)
	if s.data == nil || s.data.Sets[configSet] == nil {
		return "", "", "", false
	}
	prefix := "/api/" + category
	if path == "" {
		return configSet, prefix, "/", true
	}
	return configSet, prefix, "/" + path, true
}

func (s *Server) uiRoute(requestPath string) (string, string) {
	cleanPath := urlpath.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	trimmed := strings.TrimPrefix(cleanPath, "/")
	if trimmed != "" {
		first, rest, _ := strings.Cut(trimmed, "/")
		configSet := configSetForCategory(first)
		if configSet != "main" && s.data != nil && s.data.Sets[configSet] != nil {
			if rest == "" {
				return configSet, "/"
			}
			return configSet, "/" + rest
		}
	}
	return "main", cleanPath
}

func configSetForCategory(category string) string {
	if category == "latest" {
		return "main"
	}
	return category
}

func (s *Server) serveWeb(w http.ResponseWriter, path string) {
	name := strings.TrimPrefix(urlpath.Clean(path), "/")
	if name == "" {
		name = "index.html"
	}

	content, err := fs.ReadFile(s.web, name)
	if err != nil {
		if filepath.Ext(name) != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		name = "index.html"
		content, err = fs.ReadFile(s.web, name)
		if err != nil {
			http.Error(w, "web assets are not built", http.StatusInternalServerError)
			return
		}
	}

	if contentType := mime.TypeByExtension(filepath.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	_, _ = w.Write(content)
}

func (s *Server) runtime(w http.ResponseWriter, configSet string) {
	dnsRefresh := DNSRefreshStatus{Enabled: false}
	if s.data.Runtime != nil {
		dnsRefresh = s.data.Runtime.Status(configSet)
	}
	if s.data.DNSStatus != nil {
		dnsRefresh = s.data.DNSStatus(configSet)
	}
	var refresh SnapshotRefreshStatus
	if s.data.RefreshStatus != nil {
		refresh = s.data.RefreshStatus()
	}
	source := s.data.Sources[configSet]
	if s.cfg.SnapshotMaxAge > 0 && time.Since(source.LastSuccessAt) > s.cfg.SnapshotMaxAge {
		source.Status = "stale"
	}
	writeJSON(w, map[string]any{
		"configSet":       configSet,
		"dnsRefresh":      dnsRefresh,
		"sets":            s.data.SetInfos,
		"urls":            portalURLs(s.data.SetInfos),
		"snapshotRefresh": refresh,
		"source":          source,
	})
}

func (s *Server) catalog(data *ConfigSetData, prefix string) map[string]any {
	return map[string]any{
		"configSet": data.ConfigSet,
		"groups":    groupsWithPrefix(data.Groups, prefix),
		"sites":     summariesFromGroups(groupsWithPrefix(data.Groups, prefix)),
	}
}

func summariesFromGroups(groups []GroupSummary) []SiteSummary {
	var sites []SiteSummary
	for _, group := range groups {
		sites = append(sites, group.Sites...)
	}
	return sites
}

func groupsWithPrefix(groups []GroupSummary, prefix string) []GroupSummary {
	output := make([]GroupSummary, 0, len(groups))
	for _, group := range groups {
		next := GroupSummary{Name: group.Name, Sites: make([]SiteSummary, 0, len(group.Sites))}
		for _, site := range group.Sites {
			site.Icon = prefix + "/favicon?site=" + site.Name
			if prefix == "" {
				site.Icon = "/favicon?site=" + site.Name
			}
			next.Sites = append(next.Sites, site)
		}
		output = append(output, next)
	}
	return output
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, data *ConfigSetData, prefix string) {
	req := newExportRequest(r)
	if s.serveCachedExport(w, r, data.ConfigSet) {
		return
	}
	sites := req.selectSites(data.Sites)

	switch req.Format {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("data") == "" {
			writeJSON(w, sitesWithPrefix(sites, prefix))
			return
		}
		writeJSON(w, siteDataMap(req, sites))
	case "text", "unifi":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("filesave") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="iplist.txt"`)
		}
		_, _ = w.Write(joinedLines(req.exportValues(sites)))
	case "mikrotik":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("filesave") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="iplist.rsc"`)
		}
		_, _ = w.Write([]byte(mikrotikScript(req, sites)))
	case "ipset":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("filesave") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="iplist-ipset.conf"`)
		}
		_, _ = w.Write(joinedLines(ipsetLines(req, sites)))
	case "nfset":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("filesave") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="iplist-nfset.conf"`)
		}
		_, _ = w.Write(joinedLines(nfsetLines(req, sites)))
	case "amnezia":
		if r.URL.Query().Get("filesave") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="iplist-amnezia.json"`)
		}
		writeJSON(w, amneziaEntries(req, sites))
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func sitesWithPrefix(sites []Site, prefix string) []Site {
	output := make([]Site, 0, len(sites))
	for _, site := range sites {
		site.Icon = prefix + "/favicon?site=" + site.Name
		if prefix == "" {
			site.Icon = "/favicon?site=" + site.Name
		}
		output = append(output, site)
	}
	return output
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	icon := s.data.Icons[site]
	if icon == "" {
		icon = "generic.svg"
	}
	icon = filepath.Base(icon)
	path := filepath.Join(s.cfg.DataRoot, "storage", "icons", icon)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="8" fill="#2563eb"/><circle cx="16" cy="16" r="9" fill="none" stroke="white" stroke-width="2"/><path d="M7 16h18M16 7v18" stroke="white" stroke-width="2"/></svg>`))
		return
	}

	if contentType := mime.TypeByExtension(filepath.Ext(icon)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, path)
}

func portalURLs(sets []ConfigSetInfo) map[string]string {
	output := map[string]string{"master": "/", "main": "/"}
	for _, set := range sets {
		output[set.Key] = set.URL
	}
	return output
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func (s *Server) serveCachedExport(w http.ResponseWriter, r *http.Request, configSet string) bool {
	req := newExportRequest(r)
	if s.data.ExportCache != nil {
		if cached, ok := s.data.ExportCache.Get(configSet, req); ok {
			w.Header().Set("Content-Type", cached.ContentType)
			w.Header().Set("X-IPList-Export-Cache", "hit")
			w.Header().Set("X-IPList-Export-Generated-At", cached.GeneratedAt.Format(time.RFC3339))
			if r.URL.Query().Get("filesave") != "" {
				w.Header().Set("Content-Disposition", `attachment; filename="`+cached.Filename+`"`)
			}
			http.ServeFile(w, r, cached.Path)
			return true
		}
	}
	return false
}

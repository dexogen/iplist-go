package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type generation struct {
	data     *AllData
	handler  http.Handler
	cacheDir string
}

// LiveServer publishes complete generations. A request keeps its generation
// until its response finishes, including files served from the export cache.
type LiveServer struct {
	cfg        Config
	logger     *slog.Logger
	client     *snapshotClient
	mu         sync.RWMutex
	current    *generation
	activateMu sync.Mutex
	statusMu   sync.RWMutex
	status     SnapshotRefreshStatus
	dnsMu      sync.Mutex
	dnsCancel  context.CancelFunc
	dnsDone    chan struct{}
	dnsStore   *DNSRuntimeStore
	dnsPaused  bool
	dnsWake    chan struct{}
}

func NewLiveServer(cfg Config, logger *slog.Logger) *LiveServer {
	return &LiveServer{cfg: cfg, logger: logger, client: newSnapshotClient(cfg), dnsWake: make(chan struct{}, 1), status: SnapshotRefreshStatus{Enabled: true}}
}

func (s *LiveServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Write([]byte("ok\n"))
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		http.Error(w, "no validated snapshot available", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/readyz" {
		w.Write([]byte("ok\n"))
		return
	}
	s.current.handler.ServeHTTP(w, r)
}

func (s *LiveServer) Status() SnapshotRefreshStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

func (s *LiveServer) setStatus(update func(*SnapshotRefreshStatus)) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	update(&s.status)
}

func (s *LiveServer) dnsStatus(key string) DNSRefreshStatus {
	s.dnsMu.Lock()
	store := s.dnsStore
	s.dnsMu.Unlock()
	if store != nil {
		return store.Status(key)
	}
	return DNSRefreshStatus{Enabled: s.cfg.DNSRefreshEnabled}
}

func (s *LiveServer) data() *AllData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil
	}
	return s.current.data
}

func (s *LiveServer) buildGeneration(data *AllData, loadDNS bool) (*generation, error) {
	if loadDNS {
		data.Runtime = NewDNSRuntimeStore(s.cfg, s.logger)
		if err := data.Runtime.Load(data); err != nil {
			return nil, err
		}
	}
	data.RefreshStatus = s.Status
	data.DNSStatus = s.dnsStatus
	data.MergedSets = map[string]*ConfigSetData{}
	for _, key := range data.ConfigSetKeys() {
		data.MergedSets[key] = data.MergedConfigSet(key)
	}
	root := s.cfg.ExportCacheDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(s.cfg.DataRoot, root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "generation-")
	if err != nil {
		return nil, err
	}
	cfg := s.cfg
	cfg.ExportCacheDir = dir
	data.ExportCache = NewExportCache(cfg, data, s.logger)
	if err := data.ExportCache.RefreshAll(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &generation{data: data, handler: NewServer(s.cfg, data, s.logger), cacheDir: dir}, nil
}

func (s *LiveServer) activate(next *generation) {
	s.mu.Lock()
	old := s.current
	s.current = next
	s.mu.Unlock()
	if old != nil && old.cacheDir != next.cacheDir {
		os.RemoveAll(old.cacheDir)
	}
	s.setStatus(func(status *SnapshotRefreshStatus) { status.Revision = next.data.Revision })
}

func (s *LiveServer) loadSaved(ctx context.Context) error {
	var last error
	for _, location := range []struct{ root, name string }{{s.client.root, "current.json"}, {s.client.root, "previous.json"}, {filepath.Join(s.cfg.DataRoot, "bootstrap"), "manifest.json"}} {
		body, err := os.ReadFile(filepath.Join(location.root, location.name))
		if err != nil {
			last = err
			continue
		}
		manifest, err := parseManifest(body)
		if err != nil {
			last = err
			continue
		}
		data, err := s.client.load(ctx, manifest, location.root, false, nil)
		if err != nil {
			last = err
			continue
		}
		next, err := s.buildGeneration(data, true)
		if err != nil {
			last = err
			continue
		}
		s.activate(next)
		s.dnsMu.Lock()
		s.dnsStore = data.Runtime
		s.dnsMu.Unlock()
		s.logger.Info("saved snapshot loaded", "revision", data.Revision, "source", location.name)
		return nil
	}
	return last
}

func (s *LiveServer) stopDNS() {
	s.dnsMu.Lock()
	cancel, done := s.dnsCancel, s.dnsDone
	s.dnsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *LiveServer) refresh(ctx context.Context) (err error) {
	s.setStatus(func(status *SnapshotRefreshStatus) { status.Running = true; status.LastCheckedAt = time.Now().UTC() })
	defer func() {
		s.setStatus(func(status *SnapshotRefreshStatus) {
			status.Running = false
			if err != nil {
				status.LastError = err.Error()
			} else {
				status.LastError = ""
				status.LastSuccessAt = time.Now().UTC()
			}
		})
	}()
	body, err := s.client.get(ctx, s.cfg.SnapshotURL, 4<<20)
	if err != nil {
		return err
	}
	manifest, err := parseManifest(body)
	if err != nil {
		return err
	}
	oldData := s.data()
	data, err := s.client.load(ctx, manifest, s.client.root, true, oldData)
	if err != nil {
		return err
	}
	changed := oldData == nil || data.Revision != oldData.Revision
	if changed {
		s.dnsMu.Lock()
		s.dnsPaused = true
		s.dnsMu.Unlock()
		defer func() {
			s.dnsMu.Lock()
			s.dnsPaused = false
			s.dnsMu.Unlock()
			if s.cfg.DNSRefreshOnStart || oldData != nil {
				select {
				case s.dnsWake <- struct{}{}:
				default:
				}
			}
		}()
		s.stopDNS()
	}
	s.activateMu.Lock()
	defer s.activateMu.Unlock()
	var next *generation
	if changed {
		next, err = s.buildGeneration(data, true)
		if err != nil {
			return err
		}
	} else {
		s.mu.RLock()
		data.Runtime = s.current.data.Runtime
		data.ExportCache = s.current.data.ExportCache
		data.MergedSets = s.current.data.MergedSets
		data.RefreshStatus = s.Status
		data.DNSStatus = s.dnsStatus
		next = &generation{data: data, handler: NewServer(s.cfg, data, s.logger), cacheDir: s.current.cacheDir}
		s.mu.RUnlock()
	}
	currentPath := filepath.Join(s.client.root, "current.json")
	if previous, readErr := os.ReadFile(currentPath); readErr == nil && changed {
		if err = atomicWrite(filepath.Join(s.client.root, "previous.json"), previous); err != nil {
			if changed {
				os.RemoveAll(next.cacheDir)
			}
			return err
		}
	}
	if err = atomicWrite(currentPath, body); err != nil {
		if changed {
			os.RemoveAll(next.cacheDir)
		}
		return err
	}
	s.activate(next)
	if changed {
		s.dnsMu.Lock()
		s.dnsStore = data.Runtime
		s.dnsMu.Unlock()
		s.logger.Info("snapshot activated", "revision", data.Revision, "sets", len(data.Sets))
	} else {
		s.logger.Info("snapshot checked", "revision", data.Revision, "changed", false)
	}
	s.pruneObjects(manifest)
	return nil
}

func (s *LiveServer) pruneObjects(manifest *SnapshotManifest) {
	keep := map[string]bool{}
	for _, desc := range manifest.Sets {
		keep[filepath.Base(desc.Path)] = true
	}
	if body, err := os.ReadFile(filepath.Join(s.client.root, "previous.json")); err == nil {
		if previous, err := parseManifest(body); err == nil {
			for _, desc := range previous.Sets {
				keep[filepath.Base(desc.Path)] = true
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.client.root, "objects"))
	for _, entry := range entries {
		if !entry.IsDir() && !keep[entry.Name()] {
			os.Remove(filepath.Join(s.client.root, "objects", entry.Name()))
		}
	}
}

func (s *LiveServer) Start(ctx context.Context) {
	go func() {
		if err := s.loadSaved(ctx); err != nil {
			s.logger.Info("no saved snapshot", "error", err)
		}
		go s.dnsLoop(ctx)
		if s.data() != nil && s.cfg.DNSRefreshOnStart {
			select {
			case s.dnsWake <- struct{}{}:
			default:
			}
		}
		for {
			refreshCtx, cancel := context.WithTimeout(ctx, s.cfg.SnapshotTimeout)
			err := s.refresh(refreshCtx)
			cancel()
			if err != nil {
				s.logger.Warn("snapshot refresh failed; previous snapshot retained", "error", err)
			}
			interval := s.cfg.SnapshotInterval
			if interval <= 0 {
				interval = 30 * time.Minute
			}
			if err != nil && interval > time.Minute {
				interval = time.Minute
			}
			// Spread installations across time while keeping short test intervals bounded.
			if interval >= time.Minute {
				interval += time.Duration(rand.Float64() * .1 * float64(interval))
			}
			s.setStatus(func(status *SnapshotRefreshStatus) { status.NextRunAt = time.Now().Add(interval).UTC() })
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				s.stopDNS()
				return
			case <-timer.C:
			}
		}
	}()
}

func (s *LiveServer) dnsLoop(ctx context.Context) {
	if !s.cfg.DNSRefreshEnabled {
		return
	}
	schedule, err := parseCronSchedule(s.cfg.DNSRefreshCron)
	if err != nil {
		s.logger.Error("invalid DNS schedule", "error", err)
		return
	}
	location, err := time.LoadLocation(s.cfg.DNSRefreshTimezone)
	if err != nil {
		s.logger.Error("invalid DNS timezone", "error", err)
		return
	}
	for {
		next := schedule.Next(time.Now().In(location))
		if next.IsZero() {
			s.logger.Error("DNS schedule has no next run")
			return
		}
		s.dnsMu.Lock()
		store := s.dnsStore
		s.dnsMu.Unlock()
		if data := s.data(); data != nil && store != nil {
			for _, key := range data.ConfigSetKeys() {
				store.SetStatus(key, func(st DNSRefreshStatus) DNSRefreshStatus { st.NextRunAt = next.UTC().Format(time.RFC3339); return st })
			}
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.dnsWake:
			timer.Stop()
		}
		if err := s.runDNS(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("DNS refresh failed", "error", err)
		}
	}
}

func (s *LiveServer) runDNS(parent context.Context) error {
	s.activateMu.Lock()
	base := s.data()
	s.dnsMu.Lock()
	if s.dnsPaused {
		s.dnsMu.Unlock()
		s.activateMu.Unlock()
		return nil
	}
	if base == nil {
		s.dnsMu.Unlock()
		s.activateMu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.dnsCancel = cancel
	s.dnsDone = done
	s.dnsMu.Unlock()
	s.activateMu.Unlock()
	defer func() { cancel(); close(done) }()
	data := *base
	data.ExportCache = nil
	data.Runtime = NewDNSRuntimeStore(s.cfg, s.logger)
	if err := data.Runtime.Load(&data); err != nil {
		return err
	}
	s.dnsMu.Lock()
	s.dnsStore = data.Runtime
	s.dnsMu.Unlock()
	u := NewDNSUpdater(s.cfg, &data, data.Runtime, s.logger)
	u.runAllSets(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	next, err := s.buildGeneration(&data, false)
	if err != nil {
		return err
	}
	s.activateMu.Lock()
	defer s.activateMu.Unlock()
	current := s.data()
	if ctx.Err() != nil || current == nil || current.Revision != base.Revision {
		os.RemoveAll(next.cacheDir)
		return fmt.Errorf("DNS snapshot superseded")
	}
	data.Sources = current.Sources
	s.activate(next)
	s.logger.Info("DNS snapshot activated", "revision", data.Revision)
	return nil
}

func (s *LiveServer) MarshalJSON() ([]byte, error) { return json.Marshal(s.Status()) }

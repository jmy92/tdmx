// Package manager orchestrates download lifecycle, scheduling, and persistence.
package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/NamanBalaji/tdm/internal/download"
	"github.com/NamanBalaji/tdm/internal/store"
)

var (
	ErrNotFound           = errors.New("download not found")
	ErrInvalidPriority    = errors.New("priority must be between 1 and 10")
	ErrNoDownloader       = errors.New("no downloader can handle this URL")
	ErrDownloaderNotFound = errors.New("no downloader registered for type")
	ErrShutdownTimeout    = errors.New("shutdown timed out waiting for downloads to stop")
)

// managedDownload wraps a Download with runtime state that only the Manager.
type managedDownload struct {
	download   *download.Download
	cancel     context.CancelFunc
	tracker    *download.ProgressTracker
	initPaused bool // user paused while background initialization was running
}

// Manager orchestrates downloads. It is the SOLE OWNER of all Download
// structs — no other goroutine reads or writes Download fields without
// going through the Manager.
type Manager struct {
	mu          sync.RWMutex
	downloads   map[uuid.UUID]*managedDownload
	downloaders []download.Downloader
	dlrByType   map[string]download.Downloader // type string → downloader (for reload)
	store       store.Store
	maxConc     int

	errors       chan download.DownloadError
	scheduleCh   chan struct{}
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	wg           sync.WaitGroup
	ctx          context.Context // lifecycle context set by Start

	errMu        sync.Mutex // guards errors channel close state
	errorsClosed bool
}

func New(store store.Store, maxConcurrent int) *Manager {
	return &Manager{
		downloads:    make(map[uuid.UUID]*managedDownload),
		dlrByType:    make(map[string]download.Downloader),
		store:        store,
		maxConc:      maxConcurrent,
		errors:       make(chan download.DownloadError, 8),
		scheduleCh:   make(chan struct{}, 1),
		shutdownDone: make(chan struct{}),
	}
}

// Register adds a Downloader. Order matters: first CanHandle match wins.
func (m *Manager) Register(d download.Downloader) {
	m.downloaders = append(m.downloaders, d)
	m.dlrByType[d.Type()] = d
}

// Start loads persisted downloads from the store and starts the background run loop.
func (m *Manager) Start(ctx context.Context) error {
	downloads, err := m.store.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to load downloads: %w", err)
	}

	m.mu.Lock()
	m.ctx = ctx
	for _, dl := range downloads {
		// Downloads that were Active when the app closed are reset to Paused
		if dl.Status == download.Active {
			dl.Status = download.Paused
		}
		// Downloads that were Queued/Pending are also paused (they'll be scheduled when resumed)
		if dl.Status == download.Queued || dl.Status == download.Pending {
			dl.Status = download.Paused
		}
		// A download interrupted mid-initialization has no backend state to
		// resume from — mark it failed so the user re-adds it.
		if dl.Status == download.Initializing {
			dl.Status = download.Failed
		}

		m.downloads[dl.ID] = &managedDownload{
			download: dl,
			tracker:  download.NewProgressTracker(5 * time.Second),
		}
	}
	m.mu.Unlock()

	m.wg.Add(1)

	go m.run(ctx)

	return nil
}

// AddDownload adds a new download from a URL. threads is the requested
// per-download connection count; 0 means each downloader's configured default.
//
// The download is created and visible in the list immediately (Initializing
// status); URL probing / metadata fetching runs in the background. When the
// probe finishes successfully the download enters the scheduler; on failure
// it is marked Failed with the error surfaced through GetErrors.
func (m *Manager) AddDownload(ctx context.Context, url string, priority, threads int) (uuid.UUID, error) {
	if priority < 1 || priority > 10 {
		return uuid.Nil(), ErrInvalidPriority
	}

	// Find a Downloader that can handle this URL
	var dlr download.Downloader

	for _, d := range m.downloaders {
		if d.CanHandle(url) {
			dlr = d
			break
		}
	}

	if dlr == nil {
		return uuid.Nil(), ErrNoDownloader
	}

	now := time.Now()
	dl := &download.Download{
		ID:        uuid.New(),
		URL:       url,
		Filename:  "正在获取文件名...",
		Dir:       dlr.DownloadDir(),
		Status:    download.Initializing,
		Priority:  priority,
		Type:      dlr.Type(),
		CreatedAt: now,
	}

	if err := m.store.Save(ctx, dl); err != nil {
		return uuid.Nil(), fmt.Errorf("failed to persist download: %w", err)
	}

	m.mu.Lock()
	m.downloads[dl.ID] = &managedDownload{
		download: dl,
		tracker:  download.NewProgressTracker(5 * time.Second),
	}
	m.mu.Unlock()

	// Probe asynchronously — never block the caller on network I/O.
	go m.initializeDownload(dlr, dl, threads)

	return dl.ID, nil
}

// initializeDownload runs the downloader's Init (network probing) in the
// background, then either schedules the download or marks it failed.
func (m *Manager) initializeDownload(dlr download.Downloader, dl *download.Download, threads int) {
	// Use a detached context: initialization must survive short-lived
	// request contexts but still abort on shutdown.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(m.shutdownCtx()), 5*time.Minute)
	defer cancel()

	initialized, err := dlr.Init(ctx, dl.URL, dl.Priority, threads)
	if err != nil {
		// If the user removed/cancelled it meanwhile, don't resurrect it.
		m.mu.RLock()
		md, ok := m.downloads[dl.ID]
		stillInitializing := ok && md.download.Status == download.Initializing
		m.mu.RUnlock()

		if stillInitializing {
			m.mu.Lock()
			md.download.Status = download.Failed
			m.mu.Unlock()

			slog.Error("download initialization failed", "id", dl.ID, "url", dl.URL, "err", err)
			m.sendError(dl.ID, fmt.Errorf("failed to initialize download: %w", err))
		}

		return
	}

	initialized.CreatedAt = dl.CreatedAt

	m.mu.Lock()

	md, ok := m.downloads[dl.ID]
	if !ok || md.download.Status == download.Cancelled {
		// Removed while initializing — clean up anything Init created.
		m.mu.Unlock()
		_ = dlr.Remove(initialized)

		return
	}

	paused := md.download.Status == download.Paused // user paused it while probing
	md.initPaused = paused

	md.download.Filename = initialized.Filename
	md.download.Dir = initialized.Dir
	md.download.TotalSize = initialized.TotalSize
	md.download.State = initialized.State

	if paused {
		md.download.Status = download.Paused
	} else {
		md.download.Status = download.Queued
	}
	m.mu.Unlock()

	if saveErr := m.store.Save(context.WithoutCancel(m.shutdownCtx()), md.download); saveErr != nil {
		slog.Error("failed to save download after initialization", "id", dl.ID, "err", saveErr)
	}

	m.requestReschedule()
}

// shutdownCtx returns the manager's lifecycle context, used to derive
// detached contexts for background work.
func (m *Manager) shutdownCtx() context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.ctx
}

// GetDownload returns a copy of the download with the given ID.
func (m *Manager) GetDownload(id uuid.UUID) (download.Download, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	md, ok := m.downloads[id]
	if !ok {
		return download.Download{}, false
	}

	return *md.download, true
}

// SetPriority updates a download's priority (1-10) and re-schedules so the
// new priority takes effect immediately. Higher number = started first.
func (m *Manager) SetPriority(ctx context.Context, id uuid.UUID, priority int) error {
	if priority < 1 || priority > 10 {
		return ErrInvalidPriority
	}

	m.mu.Lock()

	md, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}

	if md.download.Status.IsTerminal() {
		m.mu.Unlock()
		return fmt.Errorf("%w: download is %s", ErrNotFound, md.download.Status)
	}

	md.download.Priority = priority
	dlCopy := *md.download
	m.mu.Unlock()

	if err := m.store.Save(ctx, &dlCopy); err != nil {
		slog.Error("failed to save download after priority change", "id", id, "err", err)
	}

	m.requestReschedule()

	return nil
}

// PauseDownload pauses an active or queued download.
func (m *Manager) PauseDownload(ctx context.Context, id uuid.UUID) {
	m.mu.Lock()

	md, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return
	}

	if md.download.Status != download.Active &&
		md.download.Status != download.Queued &&
		md.download.Status != download.Initializing {
		m.mu.Unlock()
		return
	}

	md.download.Status = download.Paused
	if md.cancel != nil {
		md.cancel()
	}
	m.mu.Unlock()

	m.requestReschedule()
}

// ResumeDownload resumes a paused or failed download.
func (m *Manager) ResumeDownload(ctx context.Context, id uuid.UUID) {
	m.mu.Lock()

	md, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return
	}

	s := md.download.Status
	if s != download.Paused && s != download.Failed {
		m.mu.Unlock()
		return
	}

	// A download paused during background initialization has no backend
	// state yet — resuming it now would start the downloader with empty
	// state. Leave it paused; initialization completion handles the
	// transition back to Queued.
	if md.initPaused && md.download.Status == download.Paused {
		m.mu.Unlock()
		return
	}

	// 恢复时把目录同步为当前设置的保存目录：改设置只影响新任务会让
	// 存量任务永远停在旧目录，与用户预期不符。恢复是最后一次让任务
	// 跟上当前设置的时机（分片状态与 Dir 无关，迁移无损）。
	if dlr, ok := m.dlrByType[md.download.Type]; ok {
		if dir := dlr.DownloadDir(); dir != "" {
			md.download.Dir = dir
		}
	}

	md.download.Status = download.Queued
	md.tracker.Reset(md.download.Downloaded, md.download.TotalSize)
	m.mu.Unlock()

	m.requestReschedule()
}

// CancelDownload cancels a download.
func (m *Manager) CancelDownload(ctx context.Context, id uuid.UUID) {
	m.mu.Lock()

	md, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return
	}

	if md.download.Status.IsTerminal() {
		m.mu.Unlock()
		return
	}

	md.download.Status = download.Cancelled
	if md.cancel != nil {
		md.cancel()
	}
	m.mu.Unlock()

	m.requestReschedule()
}

// RemoveDownload cancels and deletes a download completely.
func (m *Manager) RemoveDownload(ctx context.Context, id uuid.UUID) {
	m.mu.Lock()

	md, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return
	}

	// Cancel if running
	md.download.Status = download.Cancelled
	if md.cancel != nil {
		md.cancel()
	}

	delete(m.downloads, id)
	dlCopy := *md.download
	m.mu.Unlock()

	dlr := m.findDownloader(dlCopy.Type)
	if dlr != nil {
		if err := dlr.Remove(&dlCopy); err != nil {
			slog.Error("failed to remove download files", "err", err)
		}
	}

	if err := m.store.Delete(ctx, id); err != nil {
		slog.Error("failed to delete download from store", "id", id, "err", err)
	}

	m.requestReschedule()
}

// GetAllDownloads returns a snapshot of all downloads for the TUI.
// Uses RLock since it only reads — multiple TUI refreshes don't block each other.
func (m *Manager) GetAllDownloads() []download.DownloadInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]download.DownloadInfo, 0, len(m.downloads))
	for _, md := range m.downloads {
		info := download.DownloadInfo{
			ID:        md.download.ID,
			Filename:  md.download.Filename,
			Dir:       md.download.Dir,
			Status:    md.download.Status,
			Priority:  md.download.Priority,
			CreatedAt: md.download.CreatedAt,
		}

		if md.download.Status == download.Active {
			info.Progress = md.tracker.Snapshot()
		} else {
			// For non-active downloads, show static progress
			pct := 0.0
			if md.download.TotalSize > 0 {
				pct = float64(md.download.Downloaded) / float64(md.download.TotalSize) * 100
			}

			if md.download.Status == download.Completed {
				pct = 100
			}

			info.Progress = download.Progress{
				TotalSize:  md.download.TotalSize,
				Downloaded: md.download.Downloaded,
				Percentage: pct,
			}
		}

		result = append(result, info)
	}

	slices.SortFunc(result, func(a, b download.DownloadInfo) int {
		return cmp.Or(
			cmp.Compare(b.Priority, a.Priority),
			b.CreatedAt.Compare(a.CreatedAt),
		)
	})

	return result
}

// GetErrors returns the error channel for monitoring.
func (m *Manager) GetErrors() <-chan download.DownloadError {
	return m.errors
}

// Shutdown gracefully stops all downloads and waits for goroutines to finish.
func (m *Manager) Shutdown(ctx context.Context) error {
	var err error

	m.shutdownOnce.Do(func() {
		// Pause all active downloads
		m.mu.Lock()
		for _, md := range m.downloads {
			if md.download.Status == download.Active {
				md.download.Status = download.Paused
				if md.cancel != nil {
					md.cancel()
				}
			}
		}
		m.mu.Unlock()

		done := make(chan struct{})

		go func() {
			m.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			m.closeErrors()
		case <-ctx.Done():
			err = ErrShutdownTimeout
		}

		m.saveAll(context.WithoutCancel(ctx))

		close(m.shutdownDone)
	})

	return err
}

// Wait blocks until shutdown is complete.
func (m *Manager) Wait() {
	<-m.shutdownDone
}

// run is the Manager's background loop. It owns the context on its stack
// (never stored in the struct) and handles scheduling + periodic persistence.
func (m *Manager) run(ctx context.Context) {
	defer m.wg.Done()

	persistTicker := time.NewTicker(1 * time.Second)
	defer persistTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.scheduleCh:
			m.doSchedule(ctx)
		case <-persistTicker.C:
			m.saveAll(ctx)
		}
	}
}

// requestReschedule signals the run loop to re-evaluate scheduling.
func (m *Manager) requestReschedule() {
	select {
	case m.scheduleCh <- struct{}{}:
	default: // already one pending, no need to queue another
	}
}

// doSchedule evaluates which downloads should be active and starts/pauses accordingly.
func (m *Manager) doSchedule(ctx context.Context) {
	m.mu.Lock()

	decision := schedule(m.downloads, m.maxConc)

	for _, md := range decision.toPause {
		md.download.Status = download.Queued
		if md.cancel != nil {
			md.cancel()
		}
	}

	running := 0

	for _, md := range m.downloads {
		if md.cancel != nil {
			running++
		}
	}

	available := m.maxConc - running

	for i := range min(available, len(decision.toStart)) {
		md := decision.toStart[i]
		md.download.Status = download.Active
		md.download.StartTime = time.Now()

		runCtx, cancel := context.WithCancel(ctx)
		md.cancel = cancel

		m.wg.Add(1)

		go m.runDownload(ctx, runCtx, md)
	}

	m.mu.Unlock()
}

func (m *Manager) runDownload(managerCtx, dlCtx context.Context, md *managedDownload) {
	defer m.wg.Done()

	m.mu.RLock()
	dl := *md.download
	m.mu.RUnlock()

	dlr := m.findDownloader(dl.Type)
	if dlr == nil {
		m.mu.Lock()
		md.download.Status = download.Failed
		m.mu.Unlock()

		m.sendError(dl.ID, fmt.Errorf("%w: %q", ErrDownloaderNotFound, dl.Type))

		return
	}

	err := dlr.Start(dlCtx, &dl, func(downloaded, totalSize int64) {
		md.tracker.Update(downloaded, totalSize)
	})

	m.handleCompletion(managerCtx, md, &dl, err)
}

func (m *Manager) handleCompletion(ctx context.Context, md *managedDownload, result *download.Download, err error) {
	m.mu.Lock()

	md.download.State = result.State
	md.download.TotalSize = result.TotalSize
	md.download.Filename = result.Filename

	if err == nil {
		md.download.Status = download.Completed
		md.download.EndTime = time.Now()
		md.download.Downloaded = md.download.TotalSize
	} else if errors.Is(err, context.Canceled) {
		snap := md.tracker.Snapshot()
		md.download.Downloaded = snap.Downloaded
	} else {
		if md.download.Status == download.Active {
			md.download.Status = download.Failed
		}

		snap := md.tracker.Snapshot()
		md.download.Downloaded = snap.Downloaded
		m.sendError(md.download.ID, err)
	}

	md.cancel = nil
	dlCopy := *md.download
	m.mu.Unlock()

	if saveErr := m.store.Save(ctx, &dlCopy); saveErr != nil {
		slog.Error("failed to save download after completion", "err", saveErr)
	}

	m.requestReschedule()
}

// saveAll persists all downloads to the store.
func (m *Manager) saveAll(ctx context.Context) {
	m.mu.RLock()

	toSave := make([]*download.Download, 0, len(m.downloads))
	for _, md := range m.downloads {
		dlCopy := *md.download
		toSave = append(toSave, &dlCopy)
	}

	m.mu.RUnlock()

	for _, dl := range toSave {
		if err := m.store.Save(ctx, dl); err != nil {
			slog.Error("failed to persist download", "id", dl.ID, "err", err)
		}
	}
}

// sendError sends an error on the errors channel without blocking.
// sendError sends an error on the errors channel without blocking.
// The closed-flag mutex prevents sending on the channel after Shutdown
// closed it (background init goroutines may outlive the run loop).
func (m *Manager) sendError(id uuid.UUID, err error) {
	if err == nil {
		return
	}

	m.errMu.Lock()
	defer m.errMu.Unlock()

	if m.errorsClosed {
		return
	}

	select {
	case m.errors <- download.DownloadError{ID: id, Error: err}:
	default:
		slog.Error("error channel full, dropping error", "id", id, "err", err)
	}
}

// closeErrors marks the error channel closed and closes it. Callers must
// hold no other expectations — called exactly once from Shutdown.
func (m *Manager) closeErrors() {
	m.errMu.Lock()
	defer m.errMu.Unlock()

	if !m.errorsClosed {
		m.errorsClosed = true
		close(m.errors)
	}
}

// findDownloader finds the registered Downloader for the given type string.
func (m *Manager) findDownloader(dlType string) download.Downloader {
	return m.dlrByType[dlType]
}

// Downloaders returns the registered downloaders by type (copy of the map).
func (m *Manager) Downloaders() map[string]download.Downloader {
	out := make(map[string]download.Downloader, len(m.dlrByType))
	for k, v := range m.dlrByType {
		out[k] = v
	}

	return out
}

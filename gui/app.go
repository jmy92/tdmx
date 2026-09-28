// Package gui wires the download engine to the Wails GUI frontend.
package gui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/NamanBalaji/tdm/internal/config"
	"github.com/NamanBalaji/tdm/internal/download"
	httpdl "github.com/NamanBalaji/tdm/internal/downloaders/http"
	torrentdl "github.com/NamanBalaji/tdm/internal/downloaders/torrent"
	"github.com/NamanBalaji/tdm/internal/manager"
	"github.com/NamanBalaji/tdm/internal/store/boltdb"
	torrentPkg "github.com/NamanBalaji/tdm/pkg/torrent"
)

// DownloadDTO is the JSON-friendly view of a download sent to the frontend.
type DownloadDTO struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	Type        string `json:"type"`
	TotalSize   int64  `json:"totalSize"`
	Downloaded  int64  `json:"downloaded"`
	Percentage  float64 `json:"percentage"`
	SpeedBPS    int64  `json:"speedBPS"`
	ETASeconds  float64 `json:"etaSeconds"`
	SavePath    string `json:"savePath"`
	// FileMissing: 已完成任务的本体文件在磁盘上找不到了（被用户删除或移动）。
	// 仅 Completed 状态可能为 true，前端据此展示"文件已丢失"警告态。
	FileMissing bool `json:"fileMissing"`
	HasError    bool   `json:"hasError"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// StatsDTO is the aggregate header statistics.
type StatsDTO struct {
	Total     int   `json:"total"`
	Active    int   `json:"active"`
	Queued    int   `json:"queued"`
	Paused    int   `json:"paused"`
	Completed int   `json:"completed"`
	Failed    int   `json:"failed"`
	Cancelled int   `json:"cancelled"`
	TotalBPS  int64 `json:"totalBPS"`
}

// App is the binding surface exposed to the frontend.
type App struct {
	ctx              context.Context
	mgr              *manager.Manager
	store            *boltdb.Store
	cancel           context.CancelFunc
	httpDownloader   *httpdl.Downloader
	torrentDownloader *torrentdl.Downloader
	fileMissing      *fileMissingCache
}

// NewApp bootstraps the engine (store, torrent client, manager) shared with the TUI.
func NewApp() (*App, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// 临时分片目录强制跟随下载目录：老配置文件里持久化的是系统 Temp
	// （C 盘），空间紧张且跨盘合并要双倍占用系统盘。这里覆盖并回写，
	// 让旧配置自动迁移。
	migratedTemp := filepath.Join(cfg.HTTP.DownloadDir, ".tdm-temp")
	if cfg.HTTP.TempDir != migratedTemp {
		cfg.HTTP.TempDir = migratedTemp

		if saveErr := cfg.Save(); saveErr != nil {
			slog.Warn("migrating temp dir in config", "err", saveErr)
		}
	}

	store, err := boltdb.New(DBPath())
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	torrentClient, err := torrentPkg.NewClient(cfg.Torrent)
	if err != nil {
		store.Close()

		return nil, fmt.Errorf("creating torrent client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	mgr := manager.New(store, cfg.MaxConcurrentDownloads)

	torrentDownloader := torrentdl.New(torrentClient, cfg.Torrent.DownloadDir)
	httpDownloader := httpdl.New(cfg.HTTP)

	mgr.Register(torrentDownloader)
	mgr.Register(httpDownloader)

	if err := mgr.Start(ctx); err != nil {
		cancel()
		torrentClient.Close()
		store.Close()

		return nil, fmt.Errorf("starting manager: %w", err)
	}

	return &App{
		ctx:               ctx,
		mgr:               mgr,
		store:             store,
		cancel:            cancel,
		httpDownloader:    httpDownloader,
		torrentDownloader: torrentDownloader,
		fileMissing:       newFileMissingCache(),
	}, nil
}

// StartErrorPump forwards engine errors to the frontend as Wails events.
func (a *App) StartErrorPump() {
	go func() {
		errs := a.mgr.GetErrors()
		for {
			select {
			case <-a.ctx.Done():
				return
			case err, ok := <-errs:
				if !ok {
					return
				}

				application.Get().Event.Emit("download:error", map[string]string{
					"id":      err.ID.String(),
					"message": err.Error.Error(),
				})
			}
		}
	}()
}

// Shutdown gracefully stops the engine.
func (a *App) Shutdown() {
	// Cancel the engine context FIRST so manager goroutines (run loop and
	// active downloads) stop; otherwise Shutdown's wg.Wait blocks until the
	// 10s timeout and the window/process hangs after clicking close.
	a.cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := a.mgr.Shutdown(shutdownCtx); err != nil {
		slog.Error("gui shutdown", "err", err)
	}

	if err := a.store.Close(); err != nil {
		slog.Error("closing store", "err", err)
	}
}

// GetDownloads returns all downloads with live progress.
func (a *App) GetDownloads() []DownloadDTO {
	infos := a.mgr.GetAllDownloads()

	out := make([]DownloadDTO, 0, len(infos))
	for _, info := range infos {
		out = append(out, a.toDTO(info))
	}

	return out
}

// GetStats returns aggregate statistics for the header.
func (a *App) GetStats() StatsDTO {
	stats := StatsDTO{}

	for _, info := range a.mgr.GetAllDownloads() {
		stats.Total++

		switch info.Status {
		case download.Active:
			stats.Active++
			stats.TotalBPS += info.Progress.SpeedBPS
		case download.Queued, download.Pending, download.Initializing:
			stats.Queued++
		case download.Paused:
			stats.Paused++
		case download.Completed:
			stats.Completed++
		case download.Failed:
			stats.Failed++
		case download.Cancelled:
			stats.Cancelled++
		}
	}

	return stats
}

// AddDownload adds a new download with the given priority (1-10, 0 = default)
// and per-download thread/connection count (0 = downloader default).
func (a *App) AddDownload(url string, priority, threads int) error {
	if priority < 1 || priority > 10 {
		priority = 5
	}

	if threads < 1 || threads > 64 {
		threads = 0
	}

	_, err := a.mgr.AddDownload(a.ctx, url, priority, threads)

	return err
}

// PauseDownload pauses an active or queued download.
func (a *App) PauseDownload(id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	a.mgr.PauseDownload(a.ctx, uid)

	return nil
}

// ResumeDownload resumes a paused or failed download.
func (a *App) ResumeDownload(id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	a.mgr.ResumeDownload(a.ctx, uid)

	return nil
}

// CancelDownload cancels a running download.
func (a *App) CancelDownload(id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	a.mgr.CancelDownload(a.ctx, uid)

	return nil
}

// SetPriority updates a download's priority (1-10). Higher = started first.
func (a *App) SetPriority(id string, priority int) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	return a.mgr.SetPriority(a.ctx, uid, priority)
}

// RemoveDownload deletes a download and its files.
func (a *App) RemoveDownload(id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	a.mgr.RemoveDownload(a.ctx, uid)

	return nil
}

// Minimize minimizes the main window.
func (a *App) Minimize() {
	if win := getWindow(); win != nil {
		win.Minimise()
	}
}

// ToggleMaximize toggles maximize/restore on the main window.
func (a *App) ToggleMaximize() {
	if win := getWindow(); win != nil {
		win.ToggleMaximise()
	}
}

// Quit closes the window immediately and shuts the engine down in the
// background with a hard deadline, then force-exits the process. This
// avoids the "window closed but process hangs" freeze caused by waiting
// on slow-stopping download goroutines after the window is gone.
func (a *App) Quit() {
	go func() {
		a.cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()

		if err := a.mgr.Shutdown(shutdownCtx); err != nil {
			slog.Warn("engine shutdown on quit", "err", err)
		}

		if err := a.store.Close(); err != nil {
			slog.Warn("closing store on quit", "err", err)
		}

		// 兜底强制退出：即使有 goroutine 未响应取消，也保证进程终止。
		os.Exit(0)
	}()

	application.Get().Quit()
}

// getWindow returns the first webview window, if one exists.
func getWindow() application.Window {
	wins := application.Get().Window.GetAll()
	if len(wins) == 0 {
		return nil
	}

	return wins[0]
}

// ─── 保存目录设置 ────────────────────────────────────────

// GetSaveDir returns the current default save directory (HTTP + Torrent share it).
func (a *App) GetSaveDir() string {
	return a.httpDownloader.DownloadDir()
}

// sameDir reports whether two paths point to the same directory.
// Windows 文件系统不区分大小写，比较时忽略大小写。
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}

	return filepath.Clean(a) == filepath.Clean(b)
}

// ChooseSaveDir opens a native directory picker and, on confirmation,
// applies and persists the new save directory. Returns the chosen path or
// an empty string if the user cancelled.
func (a *App) ChooseSaveDir() (string, error) {
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("选择保存目录").
		SetDirectory(a.GetSaveDir()).
		PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("打开目录选择框失败: %w", err)
	}

	if dir == "" {
		return "", nil // user cancelled
	}

	// 目录没变：跳过保存，避免无谓的写配置及其可能引发的错误提示
	if sameDir(dir, a.GetSaveDir()) {
		return dir, nil
	}

	if err := a.applySaveDir(dir); err != nil {
		return "", fmt.Errorf("保存目录设置失败: %w", err)
	}

	return dir, nil
}

// SetSaveDir applies and persists a new save directory typed in manually.
func (a *App) SetSaveDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录不能为空")
	}

	if !filepath.IsAbs(dir) {
		return fmt.Errorf("请输入以盘符开头的绝对路径，如 E:\\00temp")
	}

	info, err := os.Stat(dir)
	if err != nil {
		// 尝试创建目录
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return fmt.Errorf("目录不存在且无法创建: %s", dir)
		}
	} else if !info.IsDir() {
		return fmt.Errorf("该路径是一个文件，不是目录: %s", dir)
	}

	// 目录没变：无需保存
	if sameDir(dir, a.GetSaveDir()) {
		return nil
	}

	return a.applySaveDir(dir)
}

// applySaveDir hot-updates downloaders and persists the config.
// The chunk temp dir always lives under the save dir (same disk), so
// merging never needs extra space on the system drive.
func (a *App) applySaveDir(dir string) error {
	a.httpDownloader.SetDownloadDir(dir)
	a.torrentDownloader.SetDownloadDir(dir)

	cfg := config.DefaultConfig()
	cfg.HTTP.DownloadDir = dir
	cfg.HTTP.TempDir = filepath.Join(dir, ".tdm-temp")
	cfg.Torrent.DownloadDir = dir

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	return nil
}

// RevealSavePath opens the file's folder in Explorer and selects the file
// (best effort: opens the folder when selection fails).
func (a *App) RevealSavePath(id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	dl, ok := a.mgr.GetDownload(uid)
	if !ok {
		return fmt.Errorf("download not found")
	}

	return openReveal(a.revealPathFor(dl.Dir, dl.Filename))
}

// fileMissingCache caches the on-disk existence check for completed
// downloads, keyed by download ID. Avoids stat-ing every completed file on
// every 500ms poll — we only re-check when the save path changes, the entry
// expires (30s), or explicitly invalidated.
type fileMissingCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]fileMissingEntry
}

type fileMissingEntry struct {
	missing bool
	path    string // path that was checked; re-check when it changes
	expires time.Time
}

const fileMissingTTL = 30 * time.Second

func newFileMissingCache() *fileMissingCache {
	return &fileMissingCache{entries: make(map[uuid.UUID]fileMissingEntry)}
}

// isMissing returns whether the file at path is absent from disk, serving
// cached results within the TTL.
func (c *fileMissingCache) isMissing(id uuid.UUID, path string) bool {
	if path == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[id]; ok && e.path == path && time.Now().Before(e.expires) {
		return e.missing
	}

	_, err := os.Stat(path)
	missing := os.IsNotExist(err)

	c.entries[id] = fileMissingEntry{missing: missing, path: path, expires: time.Now().Add(fileMissingTTL)}

	return missing
}

func (a *App) toDTO(info download.DownloadInfo) DownloadDTO {
	dto := DownloadDTO{
		ID:         info.ID.String(),
		Filename:   info.Filename,
		Status:     info.Status.String(),
		Priority:   info.Priority,
		TotalSize:  info.Progress.TotalSize,
		Downloaded: info.Progress.Downloaded,
		Percentage: info.Progress.Percentage,
		SpeedBPS:   info.Progress.SpeedBPS,
		SavePath:   filepath.Join(info.Dir, info.Filename),
	}

	// 完成任务：检查本体文件是否还在磁盘上（带缓存，不会每 500ms 打磁盘）
	if info.Status == download.Completed {
		dto.FileMissing = a.fileMissing.isMissing(info.ID, dto.SavePath)
	}

	if info.Progress.ETA > 0 {
		dto.ETASeconds = info.Progress.ETA.Seconds()
	}

	return dto
}

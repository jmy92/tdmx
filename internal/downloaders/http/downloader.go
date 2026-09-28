package http

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"uuid"

	"golang.org/x/sync/errgroup"

	"github.com/NamanBalaji/tdm/internal/config"
	"github.com/NamanBalaji/tdm/internal/download"
	httpPkg "github.com/NamanBalaji/tdm/pkg/http"
)

type Downloader struct {
	cfg    *config.HTTPConfig
	client *httpPkg.Client
}

func New(cfg *config.HTTPConfig) *Downloader {
	return &Downloader{
		cfg:    cfg,
		client: httpPkg.NewClient(),
	}
}

func (d *Downloader) Type() string { return "http" }

// DownloadDir returns the configured HTTP download directory.
func (d *Downloader) DownloadDir() string { return d.cfg.DownloadDir }

// SetDownloadDir updates the download directory at runtime. Takes effect for
// newly added downloads; in-flight downloads keep their original directory.
// The chunk temp directory moves to the same disk as the save dir — keeping
// chunks on the system temp drive makes merging require 2x free space there
// and can fill the system disk entirely.
func (d *Downloader) SetDownloadDir(dir string) {
	d.cfg.DownloadDir = dir
	d.cfg.TempDir = filepath.Join(dir, ".tdm-temp")
}

func (d *Downloader) CanHandle(url string) bool {
	return httpPkg.IsDownloadable(url)
}

func (d *Downloader) Init(ctx context.Context, url string, priority, threads int) (*download.Download, error) {
	id := uuid.New()
	tempDir := filepath.Join(d.cfg.TempDir, id.String())

	meta, err := d.probe(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to probe URL: %w", err)
	}

	if meta.totalSize <= 0 {
		return nil, fmt.Errorf("%w: %s", httpPkg.ErrUnknownSize, url)
	}

	numChunks := d.cfg.Chunks
	if threads > 0 {
		numChunks = threads
	}

	chunks := makeChunks(meta, tempDir, numChunks)

	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	st := httpState{
		Chunks:         chunks,
		SupportsRanges: meta.supportsRanges,
		TempDir:        tempDir,
		Threads:        threads,
	}

	stateBytes, err := json.Marshal(&st)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &download.Download{
		ID:        id,
		URL:       url,
		Filename:  meta.filename,
		Dir:       d.cfg.DownloadDir,
		Status:    download.Pending,
		Priority:  priority,
		Type:      "http",
		TotalSize: meta.totalSize,
		State:     stateBytes,
	}, nil
}

func (d *Downloader) Start(ctx context.Context, dl *download.Download, onProgress func(int64, int64)) error {
	var st httpState
	if err := json.Unmarshal(dl.State, &st); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	if len(st.Chunks) == 0 {
		return fmt.Errorf("%w: no chunks in download state", httpPkg.ErrUnknownSize)
	}

	// Find incomplete chunks
	var pending []int

	for i, c := range st.Chunks {
		if !c.Completed {
			pending = append(pending, i)
		}
	}

	if len(pending) == 0 {
		// 所有分片早已下完（例如上次在合并阶段失败）。必须校验最终文件
		// 是否真的存在且完整，不能直接当作成功——否则会出现"点一下恢复
		// 就显示已完成"的假成功，而磁盘上只有残缺文件。
		target := filepath.Join(dl.Dir, dl.Filename)

		if info, statErr := os.Stat(target); statErr == nil && info.Size() == dl.TotalSize {
			return nil
		}

		if mergeErr := d.merge(&st, dl.Dir, dl.Filename); mergeErr != nil {
			return mergeErr
		}

		_ = os.RemoveAll(st.TempDir)

		return nil
	}

	// Shared counter for progress reporting
	var totalDownloaded atomic.Int64
	for _, c := range st.Chunks {
		totalDownloaded.Add(c.Downloaded)
	}

	// Progress reporting goroutine
	reportCtx, reportCancel := context.WithCancel(ctx)
	reportDone := make(chan struct{})

	go func() {
		defer close(reportDone)

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-reportCtx.Done():
				return
			case <-ticker.C:
				onProgress(totalDownloaded.Load(), dl.TotalSize)
			}
		}
	}()

	// Download chunks concurrently with errgroup + semaphore
	g, gCtx := errgroup.WithContext(ctx)
	connections := d.cfg.Connections
	if st.Threads > 0 {
		connections = st.Threads
	}
	sem := make(chan struct{}, connections)

	for _, idx := range pending {
		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			return d.downloadChunk(gCtx, &st.Chunks[idx], dl.URL, st.SupportsRanges, &totalDownloaded)
		})
	}

	err := g.Wait()

	// Stop progress reporter and wait for it to exit
	reportCancel()
	<-reportDone

	// Save state for resume (whether success, cancel, or error)
	d.saveState(dl, &st)

	if err != nil {
		return err
	}

	// 落盘前的最后一步才做重名检查：此刻磁盘上仍存在同名文件时才改名
	// （"name (1).ext" 风格）。若同名文件在下载期间已被删除，则直接用
	// 原名——重命名的唯一目的是避免覆盖既有文件，而不是区分任务。
	finalName := resolveNameOnDisk(dl.Dir, dl.Filename)
	if finalName != dl.Filename {
		slog.Info("target file exists, renaming on merge", "from", dl.Filename, "to", finalName)
		dl.Filename = finalName
	}

	// All chunks complete — merge into final file
	if mergeErr := d.merge(&st, dl.Dir, dl.Filename); mergeErr != nil {
		// 合并失败（最常见：目标盘满）时保留分片，让用户恢复时可以
		// 直接重新合并，不必重新下载整个文件。
		return mergeErr
	}

	// Clean up temp directory
	_ = os.RemoveAll(st.TempDir)

	return nil
}

// resolveNameOnDisk returns filename unchanged if no such file exists in
// dir; otherwise the first free "name (n)ext" variant. Only the on-disk
// state matters — that is the moment overwriting would actually happen.
func resolveNameOnDisk(dir, filename string) string {
	if filename == "" {
		return filename
	}

	if _, err := os.Stat(filepath.Join(dir, filename)); os.IsNotExist(err) {
		return filename
	}

	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)

	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

func (d *Downloader) Remove(dl *download.Download) error {
	var st httpState
	if err := json.Unmarshal(dl.State, &st); err == nil {
		_ = os.RemoveAll(st.TempDir)
	}

	_ = os.Remove(filepath.Join(dl.Dir, dl.Filename))

	return nil
}

// downloadChunk downloads a single chunk with retry logic.
func (d *Downloader) downloadChunk(ctx context.Context, chunk *chunkState, url string, supportsRanges bool, downloaded *atomic.Int64) error {
	var lastErr error

	for attempt := range d.cfg.MaxRetries {
		err := d.transferChunk(ctx, chunk, url, supportsRanges, downloaded)
		if err == nil {
			chunk.Completed = true

			return nil
		}

		lastErr = err
		if errors.Is(err, context.Canceled) || !isRetryableError(err) {
			return err
		}

		backoff := calculateBackoff(attempt, d.cfg.RetryDelay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			slog.Debug("retrying chunk", "chunk", chunk.ID, "attempt", attempt+2)
		}
	}

	return fmt.Errorf("chunk %s failed after %d retries: %w", chunk.ID, d.cfg.MaxRetries, lastErr)
}

// transferChunk performs the actual byte transfer for a single chunk.
func (d *Downloader) transferChunk(ctx context.Context, chunk *chunkState, url string, supportsRanges bool, downloaded *atomic.Int64) error {
	currentStart := chunk.StartByte + chunk.Downloaded

	headers := map[string]string{"User-Agent": httpPkg.DefaultUserAgent}
	if supportsRanges {
		headers["Range"] = fmt.Sprintf("bytes=%d-%d", currentStart, chunk.EndByte)
	}

	conn := newConnection(url, headers, d.client, currentStart, chunk.EndByte)

	defer func() { _ = conn.close() }()

	file, err := os.OpenFile(chunk.TempFilePath, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open chunk file: %w", err)
	}

	defer func() { _ = file.Close() }()

	offset := chunk.Downloaded
	if !supportsRanges {
		offset = 0
	}

	if _, err := file.Seek(offset, 0); err != nil {
		return fmt.Errorf("failed to seek: %w", err)
	}

	buf := make([]byte, 32*1024)
	totalSize := chunk.EndByte - chunk.StartByte + 1
	remaining := totalSize - chunk.Downloaded

	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := conn.Read(ctx, buf)
		if n > 0 {
			writeN := min(int64(n), remaining)

			if _, err := file.Write(buf[:writeN]); err != nil {
				return fmt.Errorf("failed to write: %w", err)
			}

			chunk.Downloaded += writeN
			downloaded.Add(writeN)
			remaining -= writeN
		}

		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return readErr
			}

			if readErr.Error() == "EOF" || errors.Is(readErr, io.EOF) {
				break
			}

			if remaining <= 0 {
				break
			}

			return readErr
		}
	}

	// 连接提前断开但字节没下满（服务器在 Content-Length 之外截断流）时，
	// EOF-break 会把半截分片当成功，最终合并出残缺文件。这里必须校验。
	if remaining > 0 {
		return fmt.Errorf("connection closed early: chunk %s incomplete (%d/%d bytes)",
			chunk.ID, chunk.Downloaded, chunk.EndByte-chunk.StartByte+1)
	}

	return nil
}

// saveState serializes the current chunk progress back to dl.State.
func (d *Downloader) saveState(dl *download.Download, st *httpState) {
	if data, err := json.Marshal(st); err == nil {
		dl.State = data
	}
}

func makeChunks(meta *probeResult, tempDir string, numChunks int) []chunkState {
	if meta.totalSize <= 0 {
		return nil
	}

	if !meta.supportsRanges {
		id := uuid.New()

		return []chunkState{{
			ID:           id,
			StartByte:    0,
			EndByte:      meta.totalSize - 1,
			TempFilePath: filepath.Join(tempDir, id.String()),
		}}
	}

	chunkSize := meta.totalSize / int64(numChunks)
	if chunkSize <= 0 {
		id := uuid.New()

		return []chunkState{{
			ID:           id,
			StartByte:    0,
			EndByte:      meta.totalSize - 1,
			TempFilePath: filepath.Join(tempDir, id.String()),
		}}
	}

	var (
		chunks []chunkState
		start  int64
	)

	for start < meta.totalSize {
		end := min(start+chunkSize-1, meta.totalSize-1)

		id := uuid.New()
		chunks = append(chunks, chunkState{
			ID:           id,
			StartByte:    start,
			EndByte:      end,
			TempFilePath: filepath.Join(tempDir, id.String()),
		})
		start = end + 1
	}

	return chunks
}

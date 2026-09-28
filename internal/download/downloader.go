package download

import "context"

// Downloader defines the contract for a protocol-specific download strategy.
//
// Implementations handle ONLY the byte-transfer logic for their protocol.
// They do NOT manage goroutines for persistence or progress tracking — the
// Manager handles those cross-cutting concerns.
//
// The lifecycle is:
//  1. CanHandle(url) → true means this Downloader claims the URL
//  2. Init(ctx, url, priority) → creates a Download with BackendState populated
//  3. Start(ctx, dl, onProgress) → blocks until done/cancelled, calls onProgress periodically
//  4. Remove(dl) → cleans up files when user deletes a download
//
// Each Downloader receives its configuration (including download directory)
// at construction time. This avoids the Manager needing per-type config knowledge.
//
// Start is a blocking call. The Manager runs it in a goroutine and uses
// context cancellation to pause or stop it. When context is cancelled,
// Start should save resumable state into dl.State and return ctx.Err().
// Start receives a private copy of the Download; the Manager merges State,
// TotalSize, and Filename back when it returns.
type Downloader interface {
	// Type returns the protocol identifier (e.g., "http", "torrent").
	// Used to find the right Downloader when reloading persisted downloads.
	Type() string

	// CanHandle reports whether this Downloader can handle the given URL.
	// Called in registration order; first match wins.
	// MUST NOT make network requests — it runs synchronously on the
	// add-download path and needs to return instantly.
	CanHandle(url string) bool

	// DownloadDir returns the configured directory where this downloader
	// saves completed files. Used to pre-fill Download.Dir before Init.
	DownloadDir() string

	// Init creates a new Download by probing the URL for metadata.
	// It populates Filename, TotalSize, Type, Dir, and BackendState.
	// The download directory comes from the Downloader's own config.
	// threads is the requested per-download connection count; 0 means
	// the Downloader's configured default.
	// This may make network requests (e.g., HTTP HEAD, torrent metadata fetch).
	Init(ctx context.Context, url string, priority, threads int) (*Download, error)

	// Start downloads the file. It blocks until the download completes,
	// fails, or ctx is cancelled (pause/cancel).
	//
	// It calls onProgress(downloaded, totalSize) periodically to report
	// byte counts. The Manager uses these to compute speed and ETA.
	//
	// On context cancellation, Start MUST update dl.State with current
	// progress so the download can be resumed later, then return ctx.Err().
	//
	// On success, Start returns nil. On permanent failure, it returns the error.
	Start(ctx context.Context, dl *Download, onProgress func(downloaded int64, totalSize int64)) error

	// Remove deletes all files (temp and final) associated with the download.
	// Called when the user removes a download from the list.
	Remove(dl *Download) error
}

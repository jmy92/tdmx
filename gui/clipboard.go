package gui

import (
	"fmt"
	"net/url"
	"strings"
	"uuid"

	"github.com/atotto/clipboard"
)

// maxClipboardLinkLen caps how much clipboard text we're willing to treat as a link.
const maxClipboardLinkLen = 2048

// ReadClipboardLink reads the current clipboard text and returns it if it
// looks like a downloadable link (http/https URL, magnet link, or a
// .torrent file URL). Only the last line of the clipboard is considered —
// the current clipboard entry — never older clipboard history. Returns an
// empty string when the clipboard is empty, unreadable, or doesn't end
// with a download link.
func (a *App) ReadClipboardLink() string {
	text, err := clipboard.ReadAll()
	if err != nil {
		return ""
	}

	// 只取剪贴板最后 1 行（最后 1 条），不读取/分析更早的剪贴板历史。
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])

	if last == "" || len(last) > maxClipboardLinkLen {
		return ""
	}

	if IsDownloadLink(last) {
		return last
	}

	return ""
}

// CopyDownloadLink puts a download's URL on the system clipboard.
// Accepts either a download UUID or the URL itself (convenience).
func (a *App) CopyDownloadLink(idOrURL string) error {
	link := idOrURL

	if uid, err := uuid.Parse(idOrURL); err == nil {
		dl, ok := a.mgr.GetDownload(uid)
		if !ok {
			return fmt.Errorf("download not found")
		}
		link = dl.URL
	}

	if link == "" {
		return fmt.Errorf("nothing to copy")
	}

	return clipboard.WriteAll(link)
}

// IsDownloadLink reports whether s looks like a downloadable link using
// lightweight local checks only — no network requests.
func IsDownloadLink(s string) bool {
	if strings.HasPrefix(s, "magnet:?") && strings.Contains(s, "xt=") {
		return true
	}

	if strings.HasSuffix(strings.ToLower(s), ".torrent") {
		return true
	}

	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err == nil && u.Host != "" {
			return true
		}
	}

	return false
}

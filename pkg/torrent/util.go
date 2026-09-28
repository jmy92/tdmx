package torrent

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// torrentProbeTimeout caps the Content-Type probe so a slow/hung server can
// never stall download creation. The probe only runs for bare http(s) URLs
// that don't end in .torrent.
const torrentProbeTimeout = 5 * time.Second

// HasTorrentFile checks if a given URL points to a torrent file.
//
// Cheap local checks first (extension); only bare http(s) URLs fall back to
// a bounded Content-Type HEAD probe. Callers that must not block (e.g.
// CanHandle during URL routing) should use LooksLikeTorrentURL instead.
func HasTorrentFile(url string) bool {
	if strings.HasSuffix(strings.ToLower(url), ".torrent") {
		return true
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return false
	}

	client := &http.Client{Timeout: torrentProbeTimeout}

	resp, err := client.Head(url)
	if err != nil {
		return false
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("failed to close response body", "err", err)
		}
	}()

	contentType := resp.Header.Get("Content-Type")
	switch contentType {
	case "application/x-bittorrent",
		"application/torrent":
		return true
	default:
		return false
	}
}

// LooksLikeTorrentURL reports whether the URL should be routed to the
// torrent downloader using LOCAL checks only — no network I/O. Covers
// magnet links and direct .torrent file links. A bare http(s) URL whose
// server serves torrent content without the file extension cannot be
// detected locally; it is routed to HTTP instead (correct for the common
// case) and the torrent Content-Type probe stays available via
// HasTorrentFile for Init-time verification.
func LooksLikeTorrentURL(url string) bool {
	if strings.HasPrefix(url, "magnet:?") {
		return true
	}

	return strings.HasSuffix(strings.ToLower(url), ".torrent")
}

// IsValidMagnetLink checks if a given string is a valid magnet link.
func IsValidMagnetLink(urlStr string) bool {
	if !strings.HasPrefix(urlStr, "magnet:?") {
		return false
	}

	u, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	if u.Scheme != "magnet" {
		return false
	}

	params, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}

	xt := params.Get("xt")
	if xt == "" {
		return false
	}

	return strings.HasPrefix(xt, "urn:btih:")
}

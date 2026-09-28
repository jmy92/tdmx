package http

import "uuid"

// httpState is the protocol-specific state persisted in Download.State.
type httpState struct {
	Chunks         []chunkState `json:"chunks"`
	SupportsRanges bool         `json:"supportsRanges"`
	TempDir        string       `json:"tempDir"`
	// Threads is the per-download connection limit requested by the user;
	// 0 means use the downloader's configured default.
	Threads int `json:"threads,omitempty"`
}

// chunkState represents a single byte-range chunk's persistent state.
type chunkState struct {
	ID           uuid.UUID `json:"id"`
	StartByte    int64     `json:"startByte"`
	EndByte      int64     `json:"endByte"`
	Downloaded   int64     `json:"downloaded"`
	Completed    bool      `json:"completed"`
	TempFilePath string    `json:"tempFilePath"`
}

// probeResult holds metadata discovered by probing the URL.
type probeResult struct {
	filename       string
	totalSize      int64
	supportsRanges bool
}

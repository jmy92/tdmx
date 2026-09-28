package config

import (
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
)

const (
	maxConcurrentDownloads           = 3
	maxRetries                       = 3
	retryDelay                       = 2 * time.Second
	httpChunks                       = 32
	httpConnections                  = 8
	seedTorrent                      = true
	establishedConnectionsPerTorrent = 50
	halfOpenConnectionsPerTorrent    = 25
	totalHalfOpenConnections         = 100
	metainfoTimeout                  = 60 * time.Second
)

var (
	downloadDir = xdg.UserDirs.Download
	// 分片临时目录固定放在下载目录下：系统 Temp 盘（Windows 上是 C 盘）
	// 空间紧张，且合并时需要与最终文件同盘，跨盘合并要双倍占用系统盘。
	tempDir = filepath.Join(downloadDir, ".tdm-temp")
)

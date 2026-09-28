<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo/tdm-wordmark.svg">
    <img src="./assets/logo/tdm-wordmark-light-bg.svg" alt="TDM" width="380">
  </picture>
</p>

# TDM — 图形界面下载管理器

一个基于 Go + Wails v3 的跨平台下载管理器，提供图形界面（GUI）和终端界面（TUI）两种使用方式。

- **HTTP/HTTPS** 多连接分片下载，支持断点续传
- **BitTorrent** 磁力链接与种子文件下载
- 现代化的桌面应用界面，支持亮色/暗色主题

## 功能特性

### 下载能力

- **HTTP/HTTPS**
  - 单任务最多 32 分片、8 并发连接的多线程下载
  - 服务器不支持 Range 时自动降级为单连接
  - 断点续传：应用重启后从中断处继续，分片全部完成但最终文件缺失时自动重新合并
  - 分片临时目录固定在下载目录下（`<下载目录>/.tdm-temp`），合并不占用系统盘空间
  - 重名保护：目标位置存在同名文件时自动保存为 `文件名 (n).扩展名`

- **BitTorrent**（粘贴种子下载链接或磁力链接）
  - 种子文件直链与磁力链接
  - DHT、PEX、Tracker 支持
  - 下载完成后可选择做种

### 任务管理

- 优先级队列：数字越大越先开始下载（P1–P10，任务卡片上可随时调整）
- 暂停 / 继续 / 取消 / 删除
- 任务按创建时间排序，新任务排在最前
- 完成后一键打开所在目录；文件被移动或删除后任务自动标记为"已丢失"
- 从剪贴板自动识别下载链接
- 支持一次粘贴多行链接，批量创建任务

### 图形界面

- 亮色 / 暗色主题切换，自动记住选择
- 实时速度、进度、ETA 显示
- 全局总速度统计
- 保存目录可在应用内直接修改并持久化
- 无边框自绘窗口（窗口控制按钮在页面内）

## 从源码构建

要求：Go 1.24+（Windows 打包推荐 Go 1.27+）。

### GUI 版本（Windows）

```powershell
git clone https://github.com/jmy92/tdmx.git
cd tdmx

# 前端资源（frontend/dist 是 go:embed 的目录，修改前端后需重新同步）
Copy-Item cmd\tdm-gui\frontend\* cmd\tdm-gui\frontend\dist\

# 嵌入图标 / 版本信息 / manifest（资源文件 rsrc_windows_amd64.syso 已随仓库提供，
# 修改 winres/winres.json 或图标后需重新生成）
go-winres make --in winres\winres.json --out cmd\tdm-gui\rsrc --arch amd64 --no-suffix
Move-Item cmd\tdm-gui\rsrc cmd\tdm-gui\rsrc_windows_amd64.syso -Force

# 打包（无控制台窗口的 GUI 程序）
go build -trimpath -ldflags "-s -w -H windowsgui" -o tdm-gui.exe ./cmd/tdm-gui
```

> Windows 资源说明：Wails v3 从 exe 资源中加载 **ID=3** 的图标作为窗口/任务栏图标，
> `winres/winres.json` 中的图标组已命名为 `#3`，文件资源管理器中的图标也来自同一份资源。

### TUI 版本

```bash
go build -o tdm
./tdm
```

## 配置

配置文件位于 `~/.config/tdm`（Linux/macOS）或 `%LOCALAPPDATA%/tdm`（Windows），首次运行自动生成。
GUI 内修改保存目录等设置后会自动回写。

```yaml
maxConcurrentDownloads: 3            # 同时下载的任务数

http:
  dir: "下载目录"                    # 新任务的保存目录（默认：系统下载目录）
  tempDir: "下载目录/.tdm-temp"      # 分片临时目录（默认跟随下载目录，避免跨盘合并）
  connections: 8                     # 每个任务的并发连接数
  maxChunks: 32                      # 单任务最大分片数
  maxRetries: 3                      # 分片失败重试次数
  retryDelay: 2s                     # 重试间隔

torrent:
  dir: "下载目录"                    # 种子下载目录
  seed: true                         # 下载完成后是否做种
  establishedConnectionsPerTorrent: 50
  halfOpenConnectionsPerTorrent: 25
  totalHalfOpenConnections: 100
  disableDht: false                  # 禁用 DHT
  disablePex: false                  # 禁用 PEX
  disableTrackers: false             # 禁用 Tracker
  disableIPv6: false
  metainfoTimeout: 60s               # 获取种子元数据超时
```

TUI 还支持命令行参数（优先级高于配置文件），详见 `tdm -h`。

## 项目结构

```
cmd/tdm-gui/        GUI 程序入口 + 内嵌前端资源
gui/                Wails 绑定层（引擎 ↔ 前端的桥接）
internal/
  config/           配置加载与默认值
  download/         下载核心数据模型与进度追踪
  downloaders/http/ HTTP 多线程下载引擎（分片/合并/续传）
  downloaders/torrent/ BitTorrent 下载引擎
  manager/          任务调度与生命周期管理
  store/boltdb/     任务持久化（BoltDB）
  tui/              终端界面
pkg/
  http/             HTTP 客户端封装
  torrent/          torrent 客户端封装
winres/             Windows 资源配置（图标/版本/manifest）
assets/icon/        应用图标源文件与各尺寸 PNG/ICO
```

## 致谢

本项目基于 [NamanBalaji/tdm](https://github.com/NamanBalaji/tdm) 改造，在其终端下载引擎之上增加了图形界面。

## License

[MIT](./LICENSE)

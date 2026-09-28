package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// openReveal opens the folder containing path in the OS file manager,
// selecting the file where the platform supports it (Windows/macOS).
//
// If path itself does not exist (e.g. the download is still in progress and
// the final file has not been merged yet), the parent directory is opened
// instead — passing a non-existent path to `explorer /select,` makes Explorer
// open an unrelated default folder, which is exactly the "wrong folder" bug.
func openReveal(path string) error {
	target := path

	if _, err := os.Stat(path); err != nil {
		// 目标不存在（未完成/占位名/旧记录）：退回到打开所在目录
		target = dirOf(path)
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("目录不存在: %s", target)
		}
	}

	switch runtime.GOOS {
	case "windows":
		// explorer 要求反斜杠路径
		p := strings.ReplaceAll(target, "/", "\\")

		// 目标本身就是目录（如 BT 任务）时直接打开它；
		// /select 对目录会打开其父目录，不是用户期望的行为。
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			_ = exec.Command("explorer", p).Start()

			return nil
		}

		// explorer /select 后面的逗号必须紧跟参数；explorer 退出码非零属正常
		// 行为（即使成功），忽略退出码
		_ = exec.Command("explorer", "/select,", p).Start()

		return nil
	case "darwin":
		return exec.Command("open", "-R", target).Start()
	default:
		// Linux: 打开所在目录
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			return exec.Command("xdg-open", target).Start()
		}

		return exec.Command("xdg-open", dirOf(target)).Start()
	}
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}

	return "."
}

// revealPathFor resolves the on-disk path for a download, falling back to the
// current default save directory when the record has no directory (downloads
// created before the save-dir feature was introduced have an empty Dir).
func (a *App) revealPathFor(dlDir, filename string) string {
	dir := dlDir
	if dir == "" {
		dir = a.GetSaveDir()
	}

	return filepath.Join(dir, filename)
}

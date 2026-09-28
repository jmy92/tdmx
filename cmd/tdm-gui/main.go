package main

import (
	"embed"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/NamanBalaji/tdm/gui"
	"github.com/NamanBalaji/tdm/internal/logger"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	homeDir, _ := os.UserHomeDir()
	configDir := filepath.Join(homeDir, ".tdm")

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		log.Fatalf("Error creating config directory: %v\n", err)
	}

	if err := logger.Init(false, filepath.Join(configDir, "tdm-gui.log")); err != nil {
		log.Fatalf("Error initializing logging: %v\n", err)
	}

	defer logger.Close()

	app, err := gui.NewApp()
	if err != nil {
		log.Fatalf("Error starting engine: %v\n", err)
	}
	defer app.Shutdown()

	wailsApp := application.New(application.Options{
		Name:        "TDM",
		Description: "TDM - Terminal Download Manager",
		Services: []application.Service{
			application.NewService(app),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.StartErrorPump()

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "TDM - 下载管理器",
		Width:           1180,
		Height:          760,
		MinWidth:        920,
		MinHeight:       620,
		InitialPosition: application.WindowCentered,
		URL:             "/",
		Frameless:       true, // 自绘标题栏（最小化/最大化/关闭按钮在页面内）
		// 亮色模式背景（与 body.theme-light 的 --bg 一致）
		BackgroundColour: application.NewRGBA(0xf5, 0xf6, 0xfa, 255),
	})

	if err := wailsApp.Run(); err != nil {
		slog.Error("wails run", "err", err)
	}
}

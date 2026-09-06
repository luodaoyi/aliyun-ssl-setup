package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// webviewUserDataPath WebView2 用户数据目录。
// 不使用默认的 %APPDATA%\[BinaryName.exe]：该目录一旦残留损坏的缓存，
// 渲染进程会反复崩溃（kind 4/6），窗口表现为全白且无法恢复。
func webviewUserDataPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "sslpanel", "webview2")
}

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "SSL 证书面板",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		Bind:      []interface{}{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			// 用固定的独立数据目录，避免 %APPDATA%\[BinaryName.exe] 下残留的
			// WebView2 缓存损坏时导致渲染进程反复崩溃（表现为窗口全白）
			WebviewUserDataPath: webviewUserDataPath(),
			// 本机同时存在 ToDesk / AskLink 虚拟显示适配器 + 老 NVIDIA/Intel 驱动，
			// WebView2 的 GPU 进程反复崩溃（日志 kind 4/6，msedge.dll 0x80000003），
			// 强制软件渲染并关闭 RendererCodeIntegrity 以稳定运行。
			WebviewGpuIsDisabled:                true,
			WebviewDisableRendererCodeIntegrity: true,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

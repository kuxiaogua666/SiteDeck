//go:build !headless

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"sitedeck/internal/agentui"
	"sitedeck/internal/desktop"
	"sitedeck/internal/filelock"
)

func runAgentUI(ctx context.Context, args []string) (err error) {
	if runtime.GOOS == "windows" {
		desktop.HideOwnConsole()
		defer func() {
			if err != nil && !errors.Is(err, flag.ErrHelp) {
				desktop.Alert("Agent 界面未能启动：\n" + err.Error())
			}
		}()
	}
	fs := flag.NewFlagSet("site-agent ui", flag.ContinueOnError)
	configFile := fs.String("config", "", "Agent configuration file; defaults to site-agent.json beside the executable")
	directory := fs.String("data-dir", "", "private data directory (default: user configuration directory)")
	listen := fs.String("listen", "127.0.0.1:48767", "loopback UI listener")
	browser := fs.Bool("browser", false, "use default browser instead of native Windows window")
	noOpen := fs.Bool("no-open", false, "start UI server without opening a window")
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *directory == "" {
		*directory, err = agentui.DefaultDirectory()
		if err != nil {
			return err
		}
	}
	*directory, err = filepath.Abs(*directory)
	if err != nil {
		return err
	}
	lease, lockErr := filelock.Acquire(filepath.Join(*directory, "ui.lock"))
	if lockErr != nil {
		descriptor, readErr := agentui.ReadDescriptor(*directory)
		if readErr != nil {
			return fmt.Errorf("界面数据目录已被占用：%w", lockErr)
		}
		if desktop.Focus(descriptor.NativePID) {
			return nil
		}
		file, reopenErr := agentui.Reopen(descriptor)
		if reopenErr != nil {
			return reopenErr
		}
		if filepath.Dir(file) != *directory {
			return fmt.Errorf("界面启动文件不在数据目录内")
		}
		if *noOpen {
			emit(map[string]string{"url": descriptor.URL, "launchFile": file})
			return nil
		}
		return desktop.OpenBrowser(file)
	}
	defer lease.Close()
	controller, err := agentui.NewControllerWithConfig(*directory, *configFile)
	if err != nil {
		return err
	}
	server, err := agentui.Listen(ctx, controller, *listen)
	if err != nil && !visitedFlag(fs, "listen") {
		server, err = agentui.Listen(ctx, controller, "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	defer server.Close()
	if controller.ShouldAutoStart() {
		if startErr := controller.Start(server.Context()); startErr != nil {
			log.Printf("自动启动失败：%s", startErr)
		}
	}
	file, err := server.NewLaunchFile()
	if err != nil {
		return err
	}
	emit(map[string]string{"url": server.URL, "dataDirectory": *directory, "accessFile": filepath.Join(*directory, "ui-access.key"), "launchFile": file})
	if !*noOpen && runtime.GOOS == "windows" && !*browser {
		if err = desktop.RunWindow(server.Context(), file, *directory, server.SetNativePID); err == nil {
			return nil
		}
		desktop.Alert("Windows 窗口未能启动。请安装 Microsoft Edge WebView2 Runtime。\n将改用默认浏览器打开界面。\n诊断日志：" + filepath.Join(*directory, "native-ui.log"))
		// A failing runtime may already have consumed the one-time launch request.
		file, err = server.NewLaunchFile()
		if err != nil {
			return err
		}
	}
	if !*noOpen {
		if err = desktop.OpenBrowser(file); err != nil {
			return err
		}
	}
	<-server.Context().Done()
	return nil
}
func visitedFlag(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
func runDesktop(args []string) error {
	fs := flag.NewFlagSet("site-agent desktop", flag.ContinueOnError)
	file := fs.String("launch-file", "", "private launch file")
	directory := fs.String("data-dir", "", "private data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *directory == "" || fs.NArg() != 0 {
		return fmt.Errorf("invalid desktop launch")
	}
	filePath, err := filepath.Abs(*file)
	if err != nil {
		return err
	}
	dirPath, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	if filepath.Dir(filePath) != dirPath {
		return fmt.Errorf("launch file must be in the private data directory")
	}
	if _, err = os.Stat(filePath); err != nil {
		return err
	}
	return desktop.Native(filePath, dirPath)
}

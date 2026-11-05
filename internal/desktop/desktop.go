package desktop

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func FileURL(path string) string {
	path, _ = filepath.Abs(path)
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) > 1 && path[1] == ':' {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func OpenBrowser(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		return openWindowsBrowser(FileURL(path))
	case "darwin":
		command = exec.Command("open", FileURL(path))
	default:
		command = exec.Command("xdg-open", FileURL(path))
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("无法打开浏览器，请手动打开 %s：%w", path, err)
	}
	go command.Wait()
	return nil
}

// RunWindow isolates the native browser runtime from the Agent service process.
func RunWindow(ctx context.Context, launchFile, directory string, onPID func(int)) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(directory, "native-ui.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	command := exec.CommandContext(ctx, executable, "desktop", "--launch-file", launchFile, "--data-dir", directory)
	prepareChild(command)
	command.Stdout = log
	command.Stderr = log
	if err = command.Start(); err != nil {
		return err
	}
	onPID(command.Process.Pid)
	err = command.Wait()
	onPID(0)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

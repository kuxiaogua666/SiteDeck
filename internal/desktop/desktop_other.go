//go:build !windows

package desktop

import (
	"fmt"
	"os/exec"
)

func HideOwnConsole()      {}
func Alert(message string) {}
func Focus(pid int) bool   { return false }
func Native(launchFile, directory string) error {
	return fmt.Errorf("原生窗口仅用于 Windows；当前平台请使用 ui 命令打开浏览器界面")
}
func prepareChild(command *exec.Cmd)         {}
func openWindowsBrowser(target string) error { return fmt.Errorf("unsupported platform") }

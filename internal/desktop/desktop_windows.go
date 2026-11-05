//go:build windows

package desktop

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var kernel32 = windows.NewLazySystemDLL("kernel32.dll")

func HideOwnConsole() {
	var processes [16]uint32
	count, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&processes[0])), 16)
	if count != 1 {
		return
	}
	window, _, _ := kernel32.NewProc("GetConsoleWindow").Call()
	if window != 0 {
		user32.NewProc("ShowWindow").Call(window, 0)
	}
}
func Alert(message string) {
	text, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("SiteDeck")
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x30)
}
func Focus(pid int) bool {
	if pid <= 0 {
		return false
	}
	found := false
	callback := syscall.NewCallback(func(window, parameter uintptr) uintptr {
		var owner uint32
		user32.NewProc("GetWindowThreadProcessId").Call(window, uintptr(unsafe.Pointer(&owner)))
		visible, _, _ := user32.NewProc("IsWindowVisible").Call(window)
		if int(owner) == pid && visible != 0 {
			user32.NewProc("ShowWindow").Call(window, 9)
			user32.NewProc("SetForegroundWindow").Call(window)
			found = true
			return 0
		}
		return 1
	})
	user32.NewProc("EnumWindows").Call(callback, 0)
	return found
}
func Native(launchFile, directory string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w := webview.NewWithOptions(webview.WebViewOptions{DataPath: filepath.Join(directory, "WebView2"), AutoFocus: true, WindowOptions: webview.WindowOptions{Title: "SiteDeck · 现场终端", Width: 1200, Height: 820, Center: true}})
	if w == nil {
		return fmt.Errorf("无法创建 Windows 窗口，请安装 Microsoft Edge WebView2 Runtime")
	}
	defer w.Destroy()
	w.SetSize(800, 540, webview.HintMin)
	// Load the executable's embedded icon for both title bar and taskbar.
	module, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	for _, item := range []struct{ kind, metric uintptr }{{0, 49}, {1, 11}} {
		size, _, _ := user32.NewProc("GetSystemMetrics").Call(item.metric)
		icon, _, _ := user32.NewProc("LoadImageW").Call(module, 1, 1, size, size, 0x8000)
		if icon != 0 {
			user32.NewProc("SendMessageW").Call(uintptr(w.Window()), 0x0080, item.kind, icon)
		}
	}
	w.Navigate(FileURL(launchFile))
	w.Run()
	return nil
}
func prepareChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
func openWindowsBrowser(target string) error {
	shell := windows.NewLazySystemDLL("shell32.dll")
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(target)
	result, _, _ := shell.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("无法打开默认浏览器（Windows 错误 %d）", result)
	}
	return nil
}

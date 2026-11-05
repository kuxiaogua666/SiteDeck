//go:build windows

package host

import (
	"context"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/UserExistsError/conpty"

	"sitedeck/internal/session"
)

func configure(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} }
func taskkill(pid int) {
	cmd := exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	configure(cmd)
	_ = cmd.Run()
}
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		taskkill(cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
}
func Shell(p Params) (*session.Session, error) {
	cmd, err := Command(p)
	if err != nil {
		return nil, err
	}
	if p.Rows == 0 {
		p.Rows = 24
	}
	if p.Cols == 0 {
		p.Cols = 80
	}
	args := make([]string, len(cmd.Args))
	for i, a := range cmd.Args {
		args[i] = syscall.EscapeArg(a)
	}
	console, err := conpty.Start(strings.Join(args, " "), conpty.ConPtyDimensions(int(p.Cols), int(p.Rows)), conpty.ConPtyWorkDir(cmd.Dir), conpty.ConPtyEnv(cmd.Env))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	closer := &consoleCloser{console: console, cancel: cancel}
	s := session.New("host", cmd.Path, console, closer, func(rows, cols uint16) error { return console.Resize(int(cols), int(rows)) })
	readDone := make(chan struct{})
	go func() { _, _ = io.Copy(s, console); close(readDone) }()
	go func() {
		code, err := console.Wait(ctx)
		if err != nil {
			code = 0xffffffff
		}
		select {
		case <-readDone:
		case <-time.After(200 * time.Millisecond):
		}
		s.End(int(int32(code)))
		_ = closer.Close()
	}()
	return s, nil
}

type consoleCloser struct {
	console *conpty.ConPty
	cancel  context.CancelFunc
	once    sync.Once
}

func (c *consoleCloser) Close() error {
	var err error
	c.once.Do(func() {
		c.cancel()
		taskkill(c.console.Pid())
		err = c.console.Close()
	})
	return err
}

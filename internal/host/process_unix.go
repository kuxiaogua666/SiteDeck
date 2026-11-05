//go:build !windows

package host

import (
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"sitedeck/internal/session"
)

func configure(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

type shellCloser struct {
	cmd      *exec.Cmd
	closePTY func() error
}

func (c shellCloser) Close() error {
	if c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	}
	return c.closePTY()
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
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: p.Rows, Cols: p.Cols})
	if err != nil {
		return nil, err
	}
	s := session.New("host", cmd.Path, f, shellCloser{cmd: cmd, closePTY: f.Close}, func(rows, cols uint16) error { return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols}) })
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = s.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
		s.End(cmd.ProcessState.ExitCode())
		_ = f.Close()
	}()
	return s, nil
}

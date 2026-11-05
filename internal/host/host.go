package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"sitedeck/internal/protocol"
)

type Params struct {
	Command string            `json:"command"`
	Argv    []string          `json:"argv"`
	Shell   string            `json:"shell"`
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
	Rows    uint16            `json:"rows"`
	Cols    uint16            `json:"cols"`
}

func Command(p Params) (*exec.Cmd, error) {
	if len(p.Argv) > 0 && p.Command != "" {
		return nil, fmt.Errorf("provide command or argv, not both")
	}
	var cmd *exec.Cmd
	if len(p.Argv) > 0 {
		cmd = exec.Command(p.Argv[0], p.Argv[1:]...)
	} else {
		shell := p.Shell
		if runtime.GOOS == "windows" {
			if shell == "" {
				shell = "powershell"
			}
			switch shell {
			case "powershell", "pwsh":
				args := []string{"-NoLogo", "-NoProfile"}
				if p.Command != "" {
					args = append(args, "-Command", p.Command)
				}
				cmd = exec.Command(shell, args...)
			case "cmd":
				args := []string{}
				if p.Command != "" {
					args = []string{"/D", "/S", "/C", p.Command}
				}
				cmd = exec.Command("cmd.exe", args...)
			default:
				return nil, fmt.Errorf("Windows shell must be powershell, pwsh or cmd")
			}
		} else {
			if shell == "" {
				shell = "/bin/sh"
			}
			args := []string{}
			if p.Command != "" {
				args = []string{"-c", p.Command}
			}
			cmd = exec.Command(shell, args...)
		}
	}
	cmd.Dir = p.Cwd
	cmd.Env = os.Environ()
	for key, value := range p.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid environment")
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd, nil
}

type Output struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	RawStdout []byte `json:"rawStdout,omitempty"`
	RawStderr []byte `json:"rawStderr,omitempty"`
	ExitCode  int    `json:"exitCode"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timedOut"`
}
type bounded struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := protocol.MaxOutput - len(b.data)
	if len(p) > left {
		p = p[:left]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func Exec(ctx context.Context, p Params) (*Output, *protocol.Error) {
	if ctx.Err() != nil {
		return nil, protocol.Fail("CANCELED", ctx.Err().Error())
	}
	if len(p.Argv) == 0 && p.Command == "" {
		return nil, protocol.Fail("INVALID_PARAMS", "command or argv is required")
	}
	cmd, err := Command(p)
	if err != nil {
		return nil, protocol.Fail("INVALID_PARAMS", err.Error())
	}
	configure(cmd)
	out, stderr := &bounded{}, &bounded{}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return nil, protocol.Fail("EXEC_FAILED", err.Error())
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			kill(cmd)
		case <-done:
		}
	}()
	err = cmd.Wait()
	close(done)
	result := &Output{Stdout: strings.ToValidUTF8(string(out.data), "�"), Stderr: strings.ToValidUTF8(string(stderr.data), "�"), ExitCode: cmd.ProcessState.ExitCode(), Truncated: out.truncated || stderr.truncated, TimedOut: ctx.Err() != nil}
	if !utf8.Valid(out.data) {
		result.RawStdout = out.data
	}
	if !utf8.Valid(stderr.data) {
		result.RawStderr = stderr.data
	}
	return result, nil
}

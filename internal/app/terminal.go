package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

func interactive(ctx context.Context, api caller, id string, readOnly bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := api.Call(ctx, "session.attach", map[string]any{"id": id, "readOnly": readOnly}); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = api.Call(cleanup, "session.detach", map[string]any{"id": id})
	}()
	fmt.Fprintln(os.Stderr, "Connected. Ctrl+] detaches this terminal.")
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), old)
	if width, height, err := term.GetSize(int(os.Stdin.Fd())); err == nil && !readOnly {
		_, _ = api.Call(ctx, "session.resize", map[string]any{"id": id, "rows": height, "cols": width})
	}
	inputError := make(chan error, 1)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(b)
			if err != nil {
				inputError <- err
				cancel()
				return
			}
			for i := 0; i < n; i++ {
				if b[i] == 0x1d {
					cancel()
					return
				}
			}
			if !readOnly && n > 0 {
				if _, err := api.Call(ctx, "session.write", map[string]any{"id": id, "data": b[:n]}); err != nil {
					inputError <- err
					cancel()
					return
				}
			}
		}
	}()
	cursor := ""
	for ctx.Err() == nil {
		poll, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, rpcErr := api.Call(poll, "session.read", map[string]any{"id": id, "cursor": cursor, "waitMs": 1000})
		cancel()
		if rpcErr != nil {
			if ctx.Err() != nil {
				break
			}
			return rpcErr
		}
		var chunk struct {
			Data     []byte `json:"data"`
			Next     string `json:"nextCursor"`
			Overflow bool   `json:"overflow"`
			EOF      bool   `json:"eof"`
			ExitCode *int   `json:"exitCode"`
		}
		if err = json.Unmarshal(raw, &chunk); err != nil {
			return err
		}
		if chunk.Overflow {
			fmt.Fprint(os.Stderr, "\r\n[output buffer overflow]\r\n")
		}
		if _, err = os.Stdout.Write(chunk.Data); err != nil {
			return err
		}
		cursor = chunk.Next
		if chunk.EOF {
			if chunk.ExitCode != nil && *chunk.ExitCode != 0 {
				return fmt.Errorf("remote shell exited with code %d", *chunk.ExitCode)
			}
			return nil
		}
	}
	select {
	case err := <-inputError:
		return err
	default:
		return nil
	}
}

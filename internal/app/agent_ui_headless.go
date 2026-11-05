//go:build headless

package app

import (
	"context"
	"flag"
	"fmt"
)

func runAgentUI(ctx context.Context, args []string) error {
	return fmt.Errorf("当前为无 UI 的嵌入式 Agent，请直接运行或使用 run/setup/pair")
}
func runDesktop(args []string) error { return fmt.Errorf("当前构建不包含桌面窗口") }
func visitedFlag(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

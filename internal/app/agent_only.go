package app

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// RunAgentOnly keeps Hub, CLI and desktop entry points out of small Agent builds.
func RunAgentOnly(args []string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runAgent(ctx, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return emitError(err, 1)
	}
	return 0
}

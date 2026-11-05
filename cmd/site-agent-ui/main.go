package main

import (
	"os"
	"sitedeck/internal/app"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "desktop" {
		args = append([]string{"ui"}, args...)
	}
	os.Exit(app.Run("agent", args))
}

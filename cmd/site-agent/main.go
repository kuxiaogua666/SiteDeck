package main

import (
	"os"
	"sitedeck/internal/app"
)

func main() { os.Exit(app.Run("agent", os.Args[1:])) }

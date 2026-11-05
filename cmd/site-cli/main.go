package main

import (
	"os"
	"sitedeck/internal/app"
)

func main() { os.Exit(app.Run("cli", os.Args[1:])) }

package main

import (
	"os"
	"sitedeck/internal/app"
)

func main() { os.Exit(app.Run("hub", os.Args[1:])) }

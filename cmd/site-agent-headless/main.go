package main

import (
	"os"
	"sitedeck/internal/app"
)

func main() { os.Exit(app.RunAgentOnly(os.Args[1:])) }

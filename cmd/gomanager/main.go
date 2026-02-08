package main

import (
	"os"

	"github.com/jmelahman/gomanager/cmd/gomanager/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}


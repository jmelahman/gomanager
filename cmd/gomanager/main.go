package main

import (
	"os"

	"github.com/jamison/gomanager/cmd/gomanager/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}


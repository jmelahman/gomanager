package cmd

import (
	"github.com/spf13/cobra"
)

// version is set by goreleaser via ldflags.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "gomanager",
	Short:   "Manage Go binaries from a curated database",
	Version: version,
	Long: `GoManager is a package manager for Go binaries.

It downloads a curated database of Go CLI tools and lets you
search, install, upgrade, and manage them.`,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

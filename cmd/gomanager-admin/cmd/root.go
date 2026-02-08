package cmd

import (
	"github.com/spf13/cobra"
)

// version is set by goreleaser via ldflags.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "gomanager-admin",
	Short:   "GoManager admin tools for database maintenance and CI",
	Version: version,
	Long: `Admin and CI tools for the GoManager database.

Provides commands for build verification, version updates, module path
fixes, and root package probing. These are typically run in CI or by
maintainers, not end users.`,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

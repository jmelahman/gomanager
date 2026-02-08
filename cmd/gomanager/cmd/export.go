package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/db"
	"github.com/jmelahman/gomanager/cmd/gomanager/internal/pkgbuild"
	"github.com/spf13/cobra"
)

var outputDir string

func init() {
	exportPkgbuildCmd.Flags().StringVarP(&outputDir, "output", "o", "", "Directory to write PKGBUILD to (default: stdout)")
	exportCmd.AddCommand(exportPkgbuildCmd)
	rootCmd.AddCommand(exportCmd)
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export binary metadata in various formats",
}

var exportPkgbuildCmd = &cobra.Command{
	Use:   "pkgbuild <name>",
	Short: "Generate an AUR PKGBUILD for a Go binary",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := db.Open()
		if err != nil {
			return err
		}
		defer conn.Close()

		b, err := db.GetByName(conn, args[0])
		if err != nil {
			return err
		}

		if outputDir != "" {
			dir := filepath.Join(outputDir, b.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("cannot create output directory: %w", err)
			}
			f, err := os.Create(filepath.Join(dir, "PKGBUILD"))
			if err != nil {
				return err
			}
			defer f.Close()
			if err := pkgbuild.Generate(f, b); err != nil {
				return err
			}
			fmt.Printf("PKGBUILD written to %s/PKGBUILD\n", dir)
			return nil
		}

		return pkgbuild.Generate(os.Stdout, b)
	},
}


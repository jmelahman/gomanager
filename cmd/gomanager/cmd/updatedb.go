package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/jamison/gomanager/cmd/gomanager/internal/db"
	"github.com/spf13/cobra"
)

// Default URL where the database is hosted (GitHub Pages or raw content).
// Override with --url flag.
var dbURL = "https://raw.githubusercontent.com/jamison/gomanager/main/database.db"

func init() {
	updateDBCmd.Flags().StringVar(&dbURL, "url", dbURL, "URL to download database.db from")
	rootCmd.AddCommand(updateDBCmd)
}

var updateDBCmd = &cobra.Command{
	Use:   "update-db",
	Short: "Download the latest binary database",
	RunE: func(cmd *cobra.Command, args []string) error {
		dest, err := db.DBPath()
		if err != nil {
			return err
		}

		fmt.Printf("Downloading database from %s ...\n", dbURL)
		resp, err := http.Get(dbURL)
		if err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
		}

		f, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("cannot write database: %w", err)
		}
		defer f.Close()

		n, err := io.Copy(f, resp.Body)
		if err != nil {
			return fmt.Errorf("write error: %w", err)
		}

		fmt.Printf("Database saved to %s (%d bytes)\n", dest, n)
		return nil
	},
}


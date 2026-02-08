package cmd

import (
	"database/sql"
	"fmt"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/spf13/cobra"
)

var (
	verifyBatchSize int
	verifyDatabase  string
	verifyReverify  bool
	verifyRecheck   bool
)

func init() {
	verifyCmd.Flags().IntVarP(&verifyBatchSize, "batch-size", "n", 50, "Number of packages to verify")
	verifyCmd.Flags().StringVarP(&verifyDatabase, "database", "d", "", "Path to database.db (default: ~/.config/gomanager/database.db)")
	verifyCmd.Flags().BoolVarP(&verifyReverify, "reverify", "r", false, "Also re-verify previously failed packages")
	verifyCmd.Flags().BoolVar(&verifyRecheck, "recheck", false, "Re-verify confirmed packages that received version updates")
	rootCmd.AddCommand(verifyCmd)
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify that packages build with go install",
	Long: `Attempt 'go install' on unverified packages and update their build status
in the database. If a build fails, it retries with CGO_ENABLED=0.

This can be run locally or in CI.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var conn *sql.DB
		var err error

		if verifyDatabase != "" {
			conn, err = db.OpenPath(verifyDatabase)
		} else {
			conn, err = db.Open()
		}
		if err != nil {
			return err
		}
		defer conn.Close()

		// Ensure schema supports 'regressed' status
		if err := db.MigrateSchema(conn); err != nil {
			return fmt.Errorf("schema migration failed: %w", err)
		}

		statuses := []string{"unknown", "pending"}
		if verifyReverify {
			statuses = append(statuses, "failed")
		}

		binaries, err := db.GetUnverified(conn, statuses, verifyBatchSize)
		if err != nil {
			return fmt.Errorf("query failed: %w", err)
		}

		// If --recheck, also include confirmed packages that got version updates
		if verifyRecheck {
			stale, err := db.GetStaleConfirmed(conn, verifyBatchSize)
			if err != nil {
				return fmt.Errorf("stale confirmed query failed: %w", err)
			}
			if len(stale) > 0 {
				fmt.Printf("Found %d confirmed packages with version updates to re-check\n", len(stale))
				binaries = append(binaries, stale...)
			}
		}

		if len(binaries) == 0 {
			fmt.Println("No packages to verify.")
			return nil
		}

		fmt.Printf("Verifying %d packages\n\n", len(binaries))

		confirmedCount, failedCount, regressedCount := 0, 0, 0

		for i, b := range binaries {
			version := b.Version
			if version == "" {
				version = "latest"
			}
			installPath := b.Package + "@" + version

			fmt.Printf("[%d/%d] %s\n", i+1, len(binaries), installPath)

			envFlags := parseEnvFlags(b.BuildFlags)

			ok, resultFlags, buildErr := tryGoInstall(installPath, envFlags)
			if !ok && len(envFlags) == 0 {
				// Retry with CGO_ENABLED=0
				fmt.Println("  Retrying with CGO_ENABLED=0...")
				ok, resultFlags, buildErr = tryGoInstall(installPath, map[string]string{"CGO_ENABLED": "0"})
			}

			if ok {
				confirmedCount++
				flagsJSON := marshalFlags(resultFlags)
				fmt.Printf("  ✓ confirmed")
				if flagsJSON != "{}" {
					fmt.Printf(" (%s)", flagsJSON)
				}
				fmt.Println()
				if err := db.UpdateBuildResult(conn, b.ID, "confirmed", flagsJSON, ""); err != nil {
					fmt.Printf("  Warning: failed to update database: %v\n", err)
				}
			} else {
				// If this was a previously confirmed package, it's a regression
				status := "failed"
				if b.BuildStatus == "confirmed" {
					status = "regressed"
					regressedCount++
					fmt.Printf("  ⚠ REGRESSED: %s\n", truncate(buildErr, 200))
				} else {
					failedCount++
					fmt.Printf("  ✗ failed: %s\n", truncate(buildErr, 200))
				}
				if err := db.UpdateBuildResult(conn, b.ID, status, b.BuildFlags, buildErr); err != nil {
					fmt.Printf("  Warning: failed to update database: %v\n", err)
				}
			}
		}

		fmt.Printf("\nDone. Confirmed: %d, Failed: %d, Regressed: %d, Total: %d\n",
			confirmedCount, failedCount, regressedCount, len(binaries))
		return nil
	},
}

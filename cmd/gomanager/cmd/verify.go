package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/db"
	"github.com/spf13/cobra"
)

var (
	verifyBatchSize int
	verifyDatabase  string
	verifyReverify  bool
)

func init() {
	verifyCmd.Flags().IntVarP(&verifyBatchSize, "batch-size", "n", 50, "Number of packages to verify")
	verifyCmd.Flags().StringVarP(&verifyDatabase, "database", "d", "", "Path to database.db (default: ~/.config/gomanager/database.db)")
	verifyCmd.Flags().BoolVarP(&verifyReverify, "reverify", "r", false, "Also re-verify previously failed packages")
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

		statuses := []string{"unknown", "pending"}
		if verifyReverify {
			statuses = append(statuses, "failed")
		}

		binaries, err := db.GetUnverified(conn, statuses, verifyBatchSize)
		if err != nil {
			return fmt.Errorf("query failed: %w", err)
		}

		if len(binaries) == 0 {
			fmt.Println("No packages to verify.")
			return nil
		}

		fmt.Printf("Verifying %d packages (statuses: %s)\n\n", len(binaries), strings.Join(statuses, ", "))

		confirmed, failed := 0, 0

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
				confirmed++
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
				failed++
				fmt.Printf("  ✗ failed: %s\n", truncate(buildErr, 200))
				if err := db.UpdateBuildResult(conn, b.ID, "failed", b.BuildFlags, buildErr); err != nil {
					fmt.Printf("  Warning: failed to update database: %v\n", err)
				}
			}
		}

		fmt.Printf("\nDone. Confirmed: %d, Failed: %d, Total: %d\n", confirmed, failed, len(binaries))
		return nil
	},
}

func tryGoInstall(installPath string, envFlags map[string]string) (ok bool, flags map[string]string, errMsg string) {
	tmpDir, err := os.MkdirTemp("", "gomanager-verify-*")
	if err != nil {
		return false, envFlags, fmt.Sprintf("cannot create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	goCmd := exec.Command("go", "install", installPath)
	goCmd.Env = append(os.Environ(), "GOBIN="+tmpDir)
	for k, v := range envFlags {
		goCmd.Env = append(goCmd.Env, k+"="+v)
	}

	var stderr bytes.Buffer
	goCmd.Stderr = &stderr

	if err := goCmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) > 5 {
			lines = lines[:5]
		}
		return false, envFlags, strings.Join(lines, " ")
	}

	return true, envFlags, ""
}

func parseEnvFlags(flagsJSON string) map[string]string {
	if flagsJSON == "" || flagsJSON == "{}" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(flagsJSON), &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func marshalFlags(flags map[string]string) string {
	if len(flags) == 0 {
		return "{}"
	}
	b, err := json.Marshal(flags)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

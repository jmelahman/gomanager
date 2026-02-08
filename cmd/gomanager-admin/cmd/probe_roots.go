package cmd

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/spf13/cobra"
)

var (
	probeBatchSize int
	probeDatabase  string
)

func init() {
	probeRootsCmd.Flags().IntVarP(&probeBatchSize, "batch-size", "n", 50, "Max repositories to probe")
	probeRootsCmd.Flags().StringVarP(&probeDatabase, "database", "d", "", "Path to database.db (default: ~/.config/gomanager/database.db)")
	rootCmd.AddCommand(probeRootsCmd)
}

var probeRootsCmd = &cobra.Command{
	Use:   "probe-roots",
	Short: "Discover root-level installable packages",
	Long: `Some Go repositories can be installed via 'go install github.com/owner/repo@latest'
even when their main.go lives in cmd/. This command finds repos where we only have
cmd/ entries and probes whether the root module path is also installable.

It reads go.mod to resolve the actual module path, handling v2+ modules
(e.g. github.com/mikefarah/yq/v4) where the install path differs from the
GitHub URL.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var conn *sql.DB
		var err error

		if probeDatabase != "" {
			conn, err = db.OpenPath(probeDatabase)
		} else {
			conn, err = db.Open()
		}
		if err != nil {
			return err
		}
		defer conn.Close()

		if err := db.MigrateSchema(conn); err != nil {
			return fmt.Errorf("schema migration failed: %w", err)
		}

		candidates, err := db.GetReposWithoutRoot(conn, probeBatchSize)
		if err != nil {
			return fmt.Errorf("query failed: %w", err)
		}

		if len(candidates) == 0 {
			fmt.Println("No repositories to probe (all repos already have root entries).")
			return nil
		}

		fmt.Printf("Probing %d repositories for root-level installability...\n\n", len(candidates))

		token := os.Getenv("GITHUB_TOKEN")
		client := &http.Client{Timeout: 10 * time.Second}
		discovered, failed := 0, 0

		for i, b := range candidates {
			owner, repo, ok := parseGitHubOwnerRepo(b.Package)
			if !ok {
				continue
			}

			modulePath, err := fetchModulePath(client, owner, repo, token)
			if err != nil {
				modulePath = "github.com/" + owner + "/" + repo
			}

			exists, err := db.PackageExists(conn, modulePath)
			if err != nil || exists {
				continue
			}

			version := b.Version
			if version == "" {
				version = "latest"
			}
			installPath := modulePath + "@" + version

			fmt.Printf("[%d/%d] Probing %s\n", i+1, len(candidates), installPath)

			ok2, resultFlags, buildErr := tryGoInstall(installPath, nil)
			if !ok2 {
				ok2, resultFlags, buildErr = tryGoInstall(installPath, map[string]string{"CGO_ENABLED": "0"})
			}

			if ok2 {
				discovered++
				flagsJSON := marshalFlags(resultFlags)

				binaryName := repo
				parts := strings.Split(modulePath, "/")
				last := parts[len(parts)-1]
				if !strings.HasPrefix(last, "v") || len(last) < 2 {
					binaryName = last
				}

				err := db.InsertBinary(conn,
					binaryName,
					modulePath,
					version,
					b.Description,
					b.RepoURL,
					b.Stars,
					true,
					"confirmed",
					flagsJSON,
				)
				if err != nil {
					fmt.Printf("  Warning: failed to insert: %v\n", err)
				} else {
					fmt.Printf("  ✓ discovered: %s", modulePath)
					if flagsJSON != "{}" {
						fmt.Printf(" (%s)", flagsJSON)
					}
					fmt.Println()
				}
			} else {
				failed++
				fmt.Printf("  ✗ not installable at root: %s\n", truncate(buildErr, 120))
			}

			if token != "" {
				time.Sleep(100 * time.Millisecond)
			} else {
				time.Sleep(2 * time.Second)
			}
		}

		fmt.Printf("\nDone. Probed %d repos, discovered %d root packages, %d not installable.\n",
			len(candidates), discovered, failed)
		return nil
	},
}

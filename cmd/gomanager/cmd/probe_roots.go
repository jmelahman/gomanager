package cmd

import (
	"bufio"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/db"
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
GitHub URL.

For example, github.com/wagoodman/dive has its entrypoint at cmd/dive/ but
'go install github.com/wagoodman/dive@latest' also works. This command detects
such cases and adds the root-level package to the database.`,
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
			// Extract owner/repo from the package path
			owner, repo, ok := parseGitHubOwnerRepo(b.Package)
			if !ok {
				continue
			}

			// Resolve the actual module path from go.mod (handles v2+ modules)
			modulePath, err := fetchModulePath(client, owner, repo, token)
			if err != nil {
				// Fallback to github.com/owner/repo
				modulePath = "github.com/" + owner + "/" + repo
			}

			// Check if the module path is already in the DB
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

			// Try go install on the module path
			ok2, resultFlags, buildErr := tryGoInstall(installPath, nil)
			if !ok2 {
				// Retry with CGO_ENABLED=0
				ok2, resultFlags, buildErr = tryGoInstall(installPath, map[string]string{"CGO_ENABLED": "0"})
			}

			if ok2 {
				discovered++
				flagsJSON := marshalFlags(resultFlags)

				// Binary name is the last non-version path segment
				binaryName := repo
				parts := strings.Split(modulePath, "/")
				last := parts[len(parts)-1]
				// If the last segment is a version (v2, v4, etc.), use the segment before
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
					true, // root-level is always primary
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

			// Rate limiting for GitHub API
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

// fetchModulePath fetches go.mod from GitHub and extracts the module directive.
// This handles v2+ modules (e.g. github.com/mikefarah/yq/v4) where the module
// path differs from the GitHub URL.
func fetchModulePath(client *http.Client, owner, repo, token string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/go.mod", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	// Request raw content to avoid base64 decoding
	req.Header.Set("Accept", "application/vnd.github.v3.raw")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module directive found in go.mod")
}

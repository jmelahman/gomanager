package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/db"
	"github.com/spf13/cobra"
)

var (
	updateBatchSize int
	updateDatabase  string
)

func init() {
	updateVersionsCmd.Flags().IntVarP(&updateBatchSize, "batch-size", "n", 100, "Max repositories to check")
	updateVersionsCmd.Flags().StringVarP(&updateDatabase, "database", "d", "", "Path to database.db (default: ~/.config/gomanager/database.db)")
	rootCmd.AddCommand(updateVersionsCmd)
}

var updateVersionsCmd = &cobra.Command{
	Use:   "update-versions",
	Short: "Check for new versions of tracked packages",
	Long: `Queries the GitHub API for the latest release of each repository
in the database. When a version changes, the package's version is updated
and updated_at is set, so the verify command with --recheck can detect
packages that need re-verification and flag regressions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var conn *sql.DB
		var err error

		if updateDatabase != "" {
			conn, err = db.OpenPath(updateDatabase)
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

		token := os.Getenv("GITHUB_TOKEN")

		binaries, err := db.ListAll(conn)
		if err != nil {
			return fmt.Errorf("failed to load packages: %w", err)
		}

		// Group packages by owner/repo to avoid duplicate API calls
		type repoGroup struct {
			owner, repo string
			binaries    []db.Binary
		}
		repoMap := make(map[string]*repoGroup)
		var repoOrder []string
		for _, b := range binaries {
			owner, repo, ok := parseGitHubOwnerRepo(b.Package)
			if !ok {
				continue
			}
			key := owner + "/" + repo
			if g, exists := repoMap[key]; exists {
				g.binaries = append(g.binaries, b)
			} else {
				repoMap[key] = &repoGroup{owner: owner, repo: repo, binaries: []db.Binary{b}}
				repoOrder = append(repoOrder, key)
			}
		}

		limit := updateBatchSize
		if limit > len(repoOrder) {
			limit = len(repoOrder)
		}

		fmt.Printf("Checking %d/%d repositories for version updates...\n\n", limit, len(repoOrder))

		updated, checked, skipped := 0, 0, 0
		client := &http.Client{Timeout: 10 * time.Second}

		for _, key := range repoOrder[:limit] {
			g := repoMap[key]
			checked++

			latestVersion, err := fetchLatestRelease(client, g.owner, g.repo, token)
			if err != nil {
				skipped++
				continue
			}
			if latestVersion == "" {
				skipped++
				continue
			}

			// Check if any binary in this repo has a different version
			needsUpdate := false
			for _, b := range g.binaries {
				if b.Version != latestVersion {
					needsUpdate = true
					break
				}
			}

			if needsUpdate {
				fmt.Printf("[%d/%d] %s/%s\n", checked, limit, g.owner, g.repo)
				for _, b := range g.binaries {
					if b.Version == latestVersion {
						continue
					}
					if err := db.UpdateVersion(conn, b.ID, latestVersion); err != nil {
						fmt.Printf("  Warning: failed to update %s: %v\n", b.Name, err)
						continue
					}
					fmt.Printf("  %s: %s → %s", b.Name, b.Version, latestVersion)
					if b.BuildStatus == "confirmed" {
						fmt.Print(" (needs re-verify)")
					}
					fmt.Println()
				}
				updated++
			}

			// Basic rate limiting: 1 request per 100ms with authenticated token,
			// sleep more without one
			if token != "" {
				time.Sleep(100 * time.Millisecond)
			} else {
				time.Sleep(2 * time.Second)
			}
		}

		fmt.Printf("\nDone. Checked %d repos, %d updated, %d skipped (no releases).\n", checked, updated, skipped)
		return nil
	},
}

func parseGitHubOwnerRepo(pkg string) (owner, repo string, ok bool) {
	if !strings.HasPrefix(pkg, "github.com/") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(pkg, "github.com/"), "/", 3)
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func fetchLatestRelease(client *http.Client, owner, repo, token string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Check for rate limiting
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			fmt.Printf("  Rate limited, waiting %ss...\n", retryAfter)
		} else {
			fmt.Println("  Rate limited, waiting 60s...")
		}
		time.Sleep(60 * time.Second)
		return "", fmt.Errorf("rate limited")
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}

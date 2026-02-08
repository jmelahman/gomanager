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
	fixPathsDatabase string
	fixPathsDryRun   bool
)

func init() {
	fixModulePathsCmd.Flags().StringVarP(&fixPathsDatabase, "database", "d", "", "Path to database.db (default: ~/.config/gomanager/database.db)")
	fixModulePathsCmd.Flags().BoolVar(&fixPathsDryRun, "dry-run", false, "Only show what would be changed, don't modify the database")
	rootCmd.AddCommand(fixModulePathsCmd)
}

var fixModulePathsCmd = &cobra.Command{
	Use:   "fix-module-paths",
	Short: "Correct package paths using go.mod module directives",
	Long: `Go modules v2+ require the major version in the import path
(e.g. github.com/mikefarah/yq/v4 instead of github.com/mikefarah/yq).

This command checks each package in the database, fetches the go.mod from
the repository, and corrects the package path if the module declaration
shows a versioned path.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var conn *sql.DB
		var err error

		if fixPathsDatabase != "" {
			conn, err = db.OpenPath(fixPathsDatabase)
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

		binaries, err := db.ListAll(conn)
		if err != nil {
			return fmt.Errorf("failed to load packages: %w", err)
		}

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

		token := os.Getenv("GITHUB_TOKEN")
		client := &http.Client{Timeout: 10 * time.Second}

		fixed, checked := 0, 0

		for _, key := range repoOrder {
			g := repoMap[key]
			checked++

			modulePath, err := fetchModulePath(client, g.owner, g.repo, token)
			if err != nil {
				continue
			}

			expectedBase := "github.com/" + g.owner + "/" + g.repo
			if modulePath == expectedBase {
				continue
			}

			for _, b := range g.binaries {
				if strings.HasPrefix(b.Package, modulePath) {
					continue
				}

				suffix := strings.TrimPrefix(b.Package, expectedBase)
				newPkg := modulePath + suffix

				exists, _ := db.PackageExists(conn, newPkg)
				if exists {
					fmt.Printf("  %s → %s (already exists, removing duplicate)\n", b.Package, newPkg)
					if !fixPathsDryRun {
						if err := db.DeleteBinary(conn, b.ID); err != nil {
							fmt.Printf("    Warning: failed to delete: %v\n", err)
						}
					}
					fixed++
					continue
				}

				fmt.Printf("  %s → %s\n", b.Package, newPkg)
				if !fixPathsDryRun {
					if err := db.UpdatePackagePath(conn, b.ID, newPkg); err != nil {
						fmt.Printf("    Warning: failed to update: %v\n", err)
						continue
					}
					if err := db.UpdateBuildResult(conn, b.ID, "unknown", b.BuildFlags, ""); err != nil {
						fmt.Printf("    Warning: failed to reset build status: %v\n", err)
					}
				}
				fixed++
			}

			if token != "" {
				time.Sleep(100 * time.Millisecond)
			} else {
				time.Sleep(2 * time.Second)
			}
		}

		if fixPathsDryRun {
			fmt.Printf("\nDry run complete. Would fix %d package paths across %d repos.\n", fixed, checked)
		} else {
			fmt.Printf("\nDone. Fixed %d package paths across %d repos.\n", fixed, checked)
		}
		return nil
	},
}

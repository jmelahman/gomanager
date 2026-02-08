package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/jmelahman/gomanager/internal/pkgbuild"
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

// licenseNames are filenames to look for as license files, in priority order.
var licenseNames = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "LICENCE.md", "LICENCE.txt", "COPYING", "COPYING.md"}

// readmeNames are filenames to look for as readme files, in priority order.
var readmeNames = []string{"README.md", "README", "README.txt", "README.rst"}

// fetchRepoFiles lists the root directory of a GitHub repository at the given
// ref (tag/branch/commit). If ref is empty, the default branch is used.
// Returns nil (no error) if the API call fails, so the caller can gracefully
// degrade to no license/readme lines.
func fetchRepoFiles(owner, repo, token, ref string) map[string]bool {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/", owner, repo)
	if ref != "" {
		url += "?ref=" + ref
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil
	}

	files := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Type == "file" {
			files[e.Name] = true
		}
	}
	return files
}

// detectRepoFiles looks up the GitHub repository for the given binary at its
// tagged version and returns PKGBUILD options with detected file info.
func detectRepoFiles(b *db.Binary) *pkgbuild.Options {
	owner, repo, ok := parseGitHubOwnerRepo(b.Package)
	if !ok {
		return nil
	}

	token := os.Getenv("GITHUB_TOKEN")
	// Use the version tag so we see files as they were at release time
	ref := b.Version
	files := fetchRepoFiles(owner, repo, token, ref)
	if files == nil {
		return nil
	}

	return buildPkgbuildOpts(files)
}

// buildPkgbuildOpts inspects a repo file listing and returns PKGBUILD options
// with detected license, readme, and go.mod presence.
func buildPkgbuildOpts(files map[string]bool) *pkgbuild.Options {
	opts := &pkgbuild.Options{
		HasGoMod: files["go.mod"],
	}
	for _, name := range licenseNames {
		if files[name] {
			opts.LicenseFile = name
			break
		}
	}
	for _, name := range readmeNames {
		if files[name] {
			opts.ReadmeFile = name
			break
		}
	}

	// Also check case-insensitive matches for common variants
	if opts.LicenseFile == "" || opts.ReadmeFile == "" {
		for f := range files {
			upper := strings.ToUpper(f)
			if opts.LicenseFile == "" && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "LICENCE") || upper == "COPYING") {
				opts.LicenseFile = f
			}
			if opts.ReadmeFile == "" && strings.HasPrefix(upper, "README") {
				opts.ReadmeFile = f
			}
		}
	}

	return opts
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

		// Fetch repo file listing to detect LICENSE and README
		opts := detectRepoFiles(b)

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
			if err := pkgbuild.Generate(f, b, opts); err != nil {
				return err
			}
			fmt.Printf("PKGBUILD written to %s/PKGBUILD\n", dir)
			return nil
		}

		return pkgbuild.Generate(os.Stdout, b, opts)
	},
}

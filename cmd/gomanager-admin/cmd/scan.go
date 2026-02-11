package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/spf13/cobra"
)

const (
	resultsPerPage   = 100
	maxPagesPerQuery = 3
	rateLimitBuffer  = 10
)

var defaultSearchQueries = []string{
	// CLI and command-line tools (topic-based)
	"topic:go+topic:cli",
	"topic:golang+topic:cli",
	"topic:go+topic:command-line",
	"topic:golang+topic:command-line",

	// General tools and utilities
	"topic:go+topic:tool",
	"topic:golang+topic:tool",
	"topic:go+topic:utility",
	"topic:golang+topic:utility",

	// Developer tools
	"topic:go+topic:devtools",
	"topic:golang+topic:devtools",
	"topic:go+topic:developer-tools",
	"topic:golang+topic:developer-tools",

	// Terminal and TUI applications
	"topic:go+topic:terminal",
	"topic:golang+topic:terminal",
	"topic:go+topic:tui",
	"topic:golang+topic:tui",

	// Code quality tools
	"topic:go+topic:linter",
	"topic:golang+topic:linter",
	"topic:go+topic:static-analysis",
	"topic:golang+topic:formatter",

	// DevOps and infrastructure
	"topic:go+topic:devops",
	"topic:golang+topic:devops",
	"topic:go+topic:infrastructure",
	"topic:golang+topic:automation",

	// Language-based queries with specific topics
	"language:go+topic:cli",
	"language:go+topic:command-line",
	"language:go+topic:tool",
	"language:go+topic:devtools",
	"language:go+topic:developer-tools",
	"language:go+topic:terminal",
	"language:go+topic:tui",
	"language:go+topic:utility",
	"language:go+topic:linter",
	"language:go+topic:static-analysis",
	"language:go+topic:formatter",
	"language:go+topic:code-generator",
	"language:go+topic:productivity",
	"language:go+topic:automation",
	"language:go+topic:monitoring",
	"language:go+topic:devops",

	// Broad language-based queries with star filters to find popular Go binaries
	// that may not have specific topic tags
	"language:go+stars:>500+topic:cli",
	"language:go+stars:>500+topic:tool",
	"language:go+stars:>1000",
}

var (
	scanDatabase    string
	scanScannedFile string
)

func init() {
	scanCmd.Flags().StringVarP(&scanDatabase, "database", "d", "./database.db", "Path to database.db")
	scanCmd.Flags().StringVar(&scanScannedFile, "scanned-repos", "./scanned_repos.json", "Path to scanned repos tracking file")
	rootCmd.AddCommand(scanCmd)
}

// githubRepo represents a repository from the GitHub search API.
type githubRepo struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Stars       int    `json:"stargazers_count"`
	HTMLURL     string `json:"html_url"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// entrypoint describes a discovered binary entrypoint in a repository.
type entrypoint struct {
	binaryName string
	pathSuffix string // e.g. "cmd/foo" or "" for root
	isPrimary  bool
}

// scanner wraps an HTTP client with GitHub token and rate-limit awareness.
type scanner struct {
	client *http.Client
	token  string
}

// apiGet performs a GET request with authorization and rate-limit handling.
// The caller is responsible for closing the response body.
func (s *scanner) apiGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "token "+s.token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}

	// Check rate limit from response headers
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		rem, _ := strconv.Atoi(remaining)
		if rem < rateLimitBuffer {
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			resetTime, _ := strconv.ParseInt(resetStr, 10, 64)
			wait := resetTime - time.Now().Unix() + 5
			if wait > 0 {
				fmt.Printf("Rate limit low (%d remaining). Sleeping %ds...\n", rem, wait)
				time.Sleep(time.Duration(wait) * time.Second)
			}
		}
	}

	return resp, nil
}

// checkRateLimit proactively checks the rate limit before starting.
func (s *scanner) checkRateLimit() {
	resp, err := s.apiGet("https://api.github.com/rate_limit")
	if err != nil {
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// searchSortOrders defines the sort strategies used for each query.
// Using multiple sort orders surfaces different repos: "stars" finds popular
// ones while "updated" finds actively maintained ones that may be less known.
var searchSortOrders = []string{"stars", "updated"}

// searchRepos discovers Go CLI repositories via the GitHub search API.
// Each query is run with multiple sort orders and filters out forks and
// archived repositories at the query level to save API calls.
func (s *scanner) searchRepos(scannedRepos map[string]bool) ([]githubRepo, error) {
	seenIDs := make(map[int]bool)
	var allRepos []githubRepo

	for _, baseQuery := range defaultSearchQueries {
		// Exclude forks and archived repos at the search level.
		query := baseQuery + "+fork:false+archived:false"

		for _, sortOrder := range searchSortOrders {
			for page := 1; page <= maxPagesPerQuery; page++ {
				url := fmt.Sprintf(
					"https://api.github.com/search/repositories?q=%s&sort=%s&order=desc&per_page=%d&page=%d",
					query, sortOrder, resultsPerPage, page,
				)

				resp, err := s.apiGet(url)
				if err != nil {
					break
				}

				if resp.StatusCode == 422 {
					// GitHub returns 422 for invalid/too-complex queries; skip.
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					break
				}

				if resp.StatusCode != 200 {
					fmt.Printf("Search failed for query=%s sort=%s page=%d: %d\n",
						baseQuery, sortOrder, page, resp.StatusCode)
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					break
				}

				var result struct {
					TotalCount int          `json:"total_count"`
					Items      []githubRepo `json:"items"`
				}
				err = json.NewDecoder(resp.Body).Decode(&result)
				resp.Body.Close()
				if err != nil {
					break
				}

				if len(result.Items) == 0 {
					break
				}

				for _, item := range result.Items {
					repoKey := item.Owner.Login + "/" + item.Name
					if !seenIDs[item.ID] && !scannedRepos[repoKey] {
						seenIDs[item.ID] = true
						allRepos = append(allRepos, item)
					}
				}

				// Respect search API rate limit (30 req/min authenticated)
				time.Sleep(2 * time.Second)

				// Stop paging if we've seen all results
				if page*resultsPerPage >= result.TotalCount {
					break
				}
			}
		}

		fmt.Printf("Query %q: collected %d unique new repos so far\n", baseQuery, len(allRepos))
	}

	return allRepos, nil
}

// checkFileExists checks whether a file exists in a GitHub repository.
func (s *scanner) checkFileExists(owner, repo, path string) bool {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	resp, err := s.apiGet(url)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == 200
}

// hasGoreleaserConfig checks if the repo has a goreleaser configuration file.
func (s *scanner) hasGoreleaserConfig(owner, repo string) bool {
	for _, path := range []string{
		".goreleaser.yml",
		".goreleaser.yaml",
		"goreleaser.yml",
		"goreleaser.yaml",
	} {
		if s.checkFileExists(owner, repo, path) {
			return true
		}
	}
	return false
}

// findEntrypoints discovers CLI binary entrypoints in a Go repository.
//
// It checks for:
//  1. Root-level main.go (always primary)
//  2. cmd/ subdirectories (primary if single entry or name matches repo)
//  3. Goreleaser config as a fallback (implies the repo produces binaries)
//  4. Homebrew formula as a fallback (strong signal for installable binaries)
func (s *scanner) findEntrypoints(owner, repo string) []entrypoint {
	var entrypoints []entrypoint

	// Check for root-level main.go (always primary)
	hasRoot := s.checkFileExists(owner, repo, "main.go")
	if hasRoot {
		entrypoints = append(entrypoints, entrypoint{
			binaryName: repo,
			pathSuffix: "",
			isPrimary:  true,
		})
	}

	// Check for cmd/ directory (standard Go project layout)
	cmdDirs := s.listSubdirs(owner, repo, "cmd")

	for _, cmd := range cmdDirs {
		isPrimary := false
		if len(cmdDirs) == 1 && !hasRoot {
			isPrimary = true
		} else if strings.EqualFold(cmd, repo) {
			isPrimary = true
		}
		entrypoints = append(entrypoints, entrypoint{
			binaryName: cmd,
			pathSuffix: "cmd/" + cmd,
			isPrimary:  isPrimary,
		})
	}

	if len(entrypoints) > 0 {
		return entrypoints
	}

	// Goreleaser fallback: implies the repo produces binaries
	if s.hasGoreleaserConfig(owner, repo) {
		return []entrypoint{{
			binaryName: repo,
			pathSuffix: "",
			isPrimary:  true,
		}}
	}

	// Homebrew formula fallback: strong signal for installable CLI tools
	if s.hasHomebrewFormula(owner, repo) {
		return []entrypoint{{
			binaryName: repo,
			pathSuffix: "",
			isPrimary:  true,
		}}
	}

	return entrypoints
}

// listSubdirs returns the names of subdirectories at the given path in a repository.
func (s *scanner) listSubdirs(owner, repo, path string) []string {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	resp, err := s.apiGet(url)
	if err != nil {
		return nil
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != 200 {
		return nil
	}

	var items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil
	}

	var dirs []string
	for _, item := range items {
		if item.Type == "dir" {
			dirs = append(dirs, item.Name)
		}
	}
	return dirs
}

// hasHomebrewFormula checks if the repo has a Homebrew formula, which is a
// strong indicator that the project produces installable binaries.
func (s *scanner) hasHomebrewFormula(owner, repo string) bool {
	for _, path := range []string{
		"Formula",
		"HomebrewFormula",
	} {
		if dirs := s.listSubdirs(owner, repo, path); len(dirs) > 0 {
			return true
		}
	}
	return false
}

// getModulePath fetches the module path from go.mod (handles v2+ modules).
func (s *scanner) getModulePath(owner, repo string) string {
	modulePath, err := fetchModulePath(s.client, owner, repo, s.token)
	if err != nil {
		return "github.com/" + owner + "/" + repo
	}
	return modulePath
}

// getLatestRelease fetches the latest release tag, or "latest" on failure.
func (s *scanner) getLatestRelease(owner, repo string) string {
	version, err := fetchLatestRelease(s.client, owner, repo, s.token)
	if err != nil || version == "" {
		return "latest"
	}
	return version
}

// loadScannedRepos loads the set of already-scanned repository keys from a JSON file.
func loadScannedRepos(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}
	var repos []string
	if err := json.Unmarshal(data, &repos); err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(repos))
	for _, r := range repos {
		m[r] = true
	}
	return m, nil
}

// saveScannedRepos writes the set of scanned repository keys to a JSON file.
func saveScannedRepos(path string, repos map[string]bool) error {
	sorted := make([]string, 0, len(repos))
	for r := range repos {
		sorted = append(sorted, r)
	}
	sort.Strings(sorted)
	data, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan GitHub for Go CLI repositories",
	Long: `Searches GitHub for Go repositories that produce CLI binaries.

Found repositories are analyzed for binary entrypoints (root main.go, cmd/
subdirectories, goreleaser configs) and added to the database. Module paths
are resolved from go.mod to handle v2+ modules correctly.

Already-scanned repositories are tracked in a JSON file to enable incremental
scanning across runs and avoid GitHub API rate limits.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := db.CreatePath(scanDatabase)
		if err != nil {
			return err
		}
		defer conn.Close()

		if err := db.InitSchema(conn); err != nil {
			return fmt.Errorf("schema init failed: %w", err)
		}

		scannedRepos, err := loadScannedRepos(scanScannedFile)
		if err != nil {
			return fmt.Errorf("failed to load scanned repos: %w", err)
		}

		existingPkgs, err := db.GetExistingPackages(conn)
		if err != nil {
			return fmt.Errorf("failed to load existing packages: %w", err)
		}

		sc := &scanner{
			client: &http.Client{Timeout: 30 * time.Second},
			token:  os.Getenv("GITHUB_TOKEN"),
		}

		sc.checkRateLimit()

		repos, err := sc.searchRepos(scannedRepos)
		if err != nil {
			return fmt.Errorf("search failed: %w", err)
		}

		newCount := 0
		fmt.Printf("\nProcessing %d new repositories...\n", len(repos))

		for i, repo := range repos {
			owner := repo.Owner.Login
			repoKey := owner + "/" + repo.Name

			fmt.Printf("[%d/%d] Scanning %s (%d stars)...\n", i+1, len(repos), repoKey, repo.Stars)

			entrypoints := sc.findEntrypoints(owner, repo.Name)
			if len(entrypoints) == 0 {
				fmt.Println("  No binaries found")
				scannedRepos[repoKey] = true
				continue
			}

			version := sc.getLatestRelease(owner, repo.Name)
			modulePath := sc.getModulePath(owner, repo.Name)

			for _, ep := range entrypoints {
				var pkgPath string
				if ep.pathSuffix != "" {
					pkgPath = modulePath + "/" + ep.pathSuffix
				} else {
					pkgPath = modulePath
				}

				if existingPkgs[pkgPath] {
					continue
				}

				repoURL := repo.HTMLURL
				if repoURL == "" {
					repoURL = "https://github.com/" + repoKey
				}

				if err := db.UpsertBinary(conn,
					ep.binaryName, pkgPath, version,
					repo.Description, repoURL, repo.Stars, ep.isPrimary,
				); err != nil {
					fmt.Printf("  Warning: failed to upsert %s: %v\n", pkgPath, err)
					continue
				}

				existingPkgs[pkgPath] = true
				newCount++
			}

			scannedRepos[repoKey] = true
		}

		if err := saveScannedRepos(scanScannedFile, scannedRepos); err != nil {
			return fmt.Errorf("failed to save scanned repos: %w", err)
		}

		fmt.Printf("\nDone. Added %d new binaries.\n", newCount)
		return nil
	},
}

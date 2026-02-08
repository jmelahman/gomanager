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

var (
	discoverMinStars int
	discoverOutput   string
	discoverNvchecker string
	discoverLimit    int
)

func init() {
	discoverCmd.Flags().IntVar(&discoverMinStars, "min-stars", 10, "Minimum stars threshold")
	discoverCmd.Flags().StringVarP(&discoverOutput, "output", "o", "", "Directory to write PKGBUILDs to")
	discoverCmd.Flags().StringVar(&discoverNvchecker, "nvchecker", "", "Path to nvchecker.toml to append entries to")
	discoverCmd.Flags().IntVarP(&discoverLimit, "limit", "n", 0, "Maximum number of candidates to output (0 = all)")
	rootCmd.AddCommand(discoverCmd)
}

// aurInfoResponse represents the AUR RPC v5 info response.
type aurInfoResponse struct {
	ResultCount int `json:"resultcount"`
	Results     []struct {
		Name string `json:"Name"`
	} `json:"results"`
}

// archPkgResponse represents the official Arch package search response.
type archPkgResponse struct {
	Results []struct {
		PkgName string `json:"pkgname"`
	} `json:"results"`
}

// batchCheckAUR checks multiple package names against the AUR in one request.
// Returns a set of names that exist in the AUR.
func batchCheckAUR(client *http.Client, names []string) map[string]bool {
	exists := make(map[string]bool)

	// AUR info endpoint supports batching with arg[]=name1&arg[]=name2...
	// Process in batches of 100 to avoid URL length limits
	for i := 0; i < len(names); i += 100 {
		end := i + 100
		if end > len(names) {
			end = len(names)
		}
		batch := names[i:end]

		var params []string
		for _, n := range batch {
			params = append(params, "arg[]="+n)
		}
		url := "https://aur.archlinux.org/rpc/v5/info?" + strings.Join(params, "&")

		resp, err := client.Get(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: AUR lookup failed: %v\n", err)
			continue
		}

		var result aurInfoResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			continue
		}

		for _, r := range result.Results {
			exists[strings.ToLower(r.Name)] = true
		}

		// Be polite to AUR API
		if end < len(names) {
			time.Sleep(500 * time.Millisecond)
		}
	}

	return exists
}

// checkOfficialRepos checks multiple package names against the official Arch repos.
// Returns a set of names that exist in official repos.
func checkOfficialRepos(client *http.Client, names []string) map[string]bool {
	exists := make(map[string]bool)

	// Official repos API supports exact name match; batch by checking multiple
	// names per request using repeated &name= params doesn't work, so we check
	// in batches using q= search and then filter exact matches.
	// Actually the API supports name=exact, so we check one at a time but can
	// be smart about it by only checking unique names.
	// To avoid excessive requests, batch names into groups and use q= search.
	for i := 0; i < len(names); i += 50 {
		end := i + 50
		if end > len(names) {
			end = len(names)
		}
		batch := names[i:end]

		for _, name := range batch {
			url := fmt.Sprintf("https://archlinux.org/packages/search/json/?name=%s", name)
			resp, err := client.Get(url)
			if err != nil {
				continue
			}

			var result archPkgResponse
			err = json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if err != nil {
				continue
			}

			for _, r := range result.Results {
				if strings.EqualFold(r.PkgName, name) {
					exists[strings.ToLower(name)] = true
				}
			}
		}

		fmt.Fprintf(os.Stderr, "Checked official repos: %d/%d\n", end, len(names))
		// Rate limit: the Arch API can be slow, be polite
		time.Sleep(1 * time.Second)
	}

	return exists
}

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Find confirmed Go packages not yet in Arch Linux repos or AUR",
	Long: `Queries the gomanager database for confirmed, primary Go packages above
a star threshold, then checks the AUR and official Arch Linux repositories
to find packages that don't have Arch packages yet.

Optionally generates PKGBUILDs and nvchecker.toml entries for the discovered
candidates.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := db.Open()
		if err != nil {
			return err
		}
		defer conn.Close()

		// Query candidates: confirmed builds, primary, with a real version, above star threshold
		binaries, err := db.ListAll(conn)
		if err != nil {
			return err
		}

		var candidates []db.Binary
		for _, b := range binaries {
			if b.BuildStatus != "confirmed" {
				continue
			}
			if !b.IsPrimary {
				continue
			}
			if b.Version == "" || b.Version == "latest" {
				continue
			}
			if b.Stars < discoverMinStars {
				continue
			}
			candidates = append(candidates, b)
		}

		fmt.Fprintf(os.Stderr, "Found %d candidates (confirmed, primary, >%d stars, versioned)\n",
			len(candidates), discoverMinStars)

		// Collect all unique names
		var names []string
		nameSet := make(map[string]bool)
		for _, b := range candidates {
			lower := strings.ToLower(b.Name)
			if !nameSet[lower] {
				nameSet[lower] = true
				names = append(names, lower)
			}
		}

		client := &http.Client{Timeout: 15 * time.Second}

		// Check AUR (fast, batched)
		fmt.Fprintf(os.Stderr, "Checking AUR for %d names...\n", len(names))
		aurExists := batchCheckAUR(client, names)
		fmt.Fprintf(os.Stderr, "  Found %d in AUR\n", len(aurExists))

		// Check official repos (slower, one-by-one)
		// Only check names not already found in AUR
		var toCheckOfficial []string
		for _, n := range names {
			if !aurExists[n] {
				toCheckOfficial = append(toCheckOfficial, n)
			}
		}

		fmt.Fprintf(os.Stderr, "Checking official repos for %d names...\n", len(toCheckOfficial))
		officialExists := checkOfficialRepos(client, toCheckOfficial)
		fmt.Fprintf(os.Stderr, "  Found %d in official repos\n", len(officialExists))

		// Filter to packages not in AUR or official repos
		var available []db.Binary
		for _, b := range candidates {
			lower := strings.ToLower(b.Name)
			if aurExists[lower] || officialExists[lower] {
				continue
			}
			available = append(available, b)
		}

		if discoverLimit > 0 && len(available) > discoverLimit {
			available = available[:discoverLimit]
		}

		fmt.Fprintf(os.Stderr, "\n%d packages not yet in Arch Linux:\n\n", len(available))

		token := os.Getenv("GITHUB_TOKEN")

		// Print results
		for _, b := range available {
			fmt.Printf("%-30s %6d stars  %s\n", b.Name, b.Stars, b.Package)
		}

		// Generate PKGBUILDs if requested
		if discoverOutput != "" {
			fmt.Fprintf(os.Stderr, "\nGenerating PKGBUILDs to %s...\n", discoverOutput)
			generated := 0
			for _, b := range available {
				opts := detectRepoFilesWithToken(&b, token)
				dir := filepath.Join(discoverOutput, b.Name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fmt.Fprintf(os.Stderr, "  Skipping %s: %v\n", b.Name, err)
					continue
				}
				f, err := os.Create(filepath.Join(dir, "PKGBUILD"))
				if err != nil {
					fmt.Fprintf(os.Stderr, "  Skipping %s: %v\n", b.Name, err)
					continue
				}
				if err := pkgbuild.Generate(f, &b, opts); err != nil {
					f.Close()
					fmt.Fprintf(os.Stderr, "  Skipping %s: %v\n", b.Name, err)
					continue
				}
				f.Close()
				generated++
			}
			fmt.Fprintf(os.Stderr, "Generated %d PKGBUILDs\n", generated)
		}

		// Append nvchecker entries if requested
		if discoverNvchecker != "" {
			fmt.Fprintf(os.Stderr, "\nAppending nvchecker entries to %s...\n", discoverNvchecker)
			f, err := os.OpenFile(discoverNvchecker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("cannot open nvchecker file: %w", err)
			}
			defer f.Close()

			for _, b := range available {
				owner, repo, ok := parseGitHubOwnerRepo(b.Package)
				if !ok {
					continue
				}

				prefix := "v"
				if !strings.HasPrefix(b.Version, "v") {
					prefix = ""
				}

				entry := fmt.Sprintf("\n[%s]\nsource = \"github\"\ngithub = \"%s/%s\"\nuse_max_tag = true\n",
					b.Name, owner, repo)
				if prefix != "" {
					entry += fmt.Sprintf("prefix = \"%s\"\n", prefix)
				}

				if _, err := f.WriteString(entry); err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: failed to write entry for %s: %v\n", b.Name, err)
				}
			}
			fmt.Fprintf(os.Stderr, "Done\n")
		}

		return nil
	},
}

// detectRepoFilesWithToken fetches repo file listing using the provided token.
func detectRepoFilesWithToken(b *db.Binary, token string) *pkgbuild.Options {
	owner, repo, ok := parseGitHubOwnerRepo(b.Package)
	if !ok {
		return nil
	}

	files := fetchRepoFiles(owner, repo, token)
	if files == nil {
		return nil
	}

	opts := &pkgbuild.Options{}
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

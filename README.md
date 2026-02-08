<p align="center">
  <img src="assets/gomanager_logo.png" alt="GoManager" width="400" />
</p>

A curated database of Go binaries with a CLI tool for searching, installing, and managing them.

GoManager automatically scans GitHub for Go CLI repositories, verifies they build with `go install`, and publishes the results to a SQLite database. A static web frontend and a Go CLI provide access to the catalog.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/preview_dark.png" />
    <source media="(prefers-color-scheme: light)" srcset="assets/preview.png" />
    <img src="assets/preview.png" alt="GoManager Preview" width="800" />
  </picture>
</p>

## Install

```bash
go install github.com/jmelahman/gomanager/cmd/gomanager@latest
```

## Usage

```
gomanager search <query>             # Search by name, package, or description
gomanager install <name>             # Install a binary by name (prompts if ambiguous)
gomanager install <package-path>     # Install a binary by full package path
gomanager list                       # List installed binaries
gomanager upgrade <name>             # Upgrade a binary to the latest version
gomanager upgrade --all              # Upgrade all installed binaries
gomanager update-db                  # Download/update the binary database
```

## Admin tools

Database maintenance and CI commands live in a separate binary:

```bash
go install github.com/jmelahman/gomanager/cmd/gomanager-admin@latest
```

```
gomanager-admin scan -d ./database.db                # Scan GitHub for Go CLI repos
gomanager-admin verify -d ./database.db -n 20        # Verify builds
gomanager-admin verify -d ./database.db --reverify   # Retry failed packages
gomanager-admin verify -d ./database.db --recheck    # Re-verify updated packages
gomanager-admin update-versions -d ./database.db     # Check for new releases
gomanager-admin probe-roots -d ./database.db         # Discover root-level packages
gomanager-admin fix-module-paths -d ./database.db    # Fix v2+ module paths
gomanager-admin export pkgbuild <name>               # Generate an AUR PKGBUILD
gomanager-admin discover --min-stars 50              # Find packages missing from Arch/AUR
gomanager-admin discover -o ./pkgbuilds              # Generate PKGBUILDs for candidates
```

## How it works

### Scanner (`gomanager-admin scan`)

Discovers Go CLI repositories on GitHub using multiple search queries. It detects binary entrypoints (`cmd/` directories, root `main.go`, goreleaser configs), reads `go.mod` to resolve v2+ module paths, and stores results in a SQLite database with metadata (stars, description, version). Already-scanned repositories are tracked in `scanned_repos.json` for incremental scanning.

Run it locally:

```bash
GITHUB_TOKEN=$(gh auth token) gomanager-admin scan --database ./database.db
```

### Build verification (`gomanager-admin verify`)

Attempts `go install` on unverified packages and updates their build status. If a build fails, it retries with `CGO_ENABLED=0`. Each binary gets a status:

| Status      | Meaning                              |
| ----------- | ------------------------------------ |
| `confirmed` | Successfully built with `go install` |
| `failed`    | Build failed (error recorded)        |
| `regressed` | Build failed (previously confirmed)  |
| `unknown`   | Not yet tested                       |
| `pending`   | Queued for verification              |

### AUR discovery (`gomanager-admin discover`)

Finds confirmed Go packages that don't yet have an Arch Linux package. Checks both the AUR (via the RPC v5 API) and official repos to filter out packages that are already available. Use it to discover candidates for new AUR PKGBUILDs:

```bash
# List candidates with >50 stars not in Arch/AUR
gomanager-admin discover --min-stars 50

# Generate PKGBUILDs and nvchecker entries
gomanager-admin discover --min-stars 50 \
  -o ./pkgbuilds \
  --nvchecker ./pkgbuilds/nvchecker.toml
```

### PKGBUILD export (`gomanager-admin export pkgbuild`)

Generates an Arch Linux PKGBUILD for any package in the database. The generated PKGBUILD clones the source via git, builds with `go build`, and installs the binary, license, and readme. It queries the GitHub API to detect the exact LICENSE and README filenames in each repository.

```bash
gomanager-admin export pkgbuild dive           # Print to stdout
gomanager-admin export pkgbuild dive -o ./out  # Write to ./out/dive/PKGBUILD
```

### Web frontend (`index.html`)

A static single-page app that loads `database.db` with [sql.js](https://sql.js.org/). Features search, filtering by build status, sortable columns, copy-to-clipboard install commands, inline editing, and light/dark mode. Host it with GitHub Pages or any static file server.

To preview locally:

```bash
./serve
```

This starts a local HTTP server on the nearest available port (starting at 8000) and opens it in your browser.

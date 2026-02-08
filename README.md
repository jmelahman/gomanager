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

## Install the CLI

```bash
go install github.com/jmelahman/gomanager/cmd/gomanager@latest
```

## Usage

```
gomanager update-db                  # Download the latest binary database
gomanager search <query>             # Search by name, package, or description
gomanager install <name>             # Install a binary with go install
gomanager list                       # List installed binaries
gomanager upgrade <name>             # Upgrade a binary to the latest version
gomanager upgrade --all              # Upgrade all installed binaries
gomanager verify -d ./database.db    # Verify builds locally
gomanager export pkgbuild <name>     # Generate an AUR PKGBUILD
```

## How it works

### Scanner (`generate_tools_json.py`)

A Python script that discovers Go CLI repositories on GitHub using multiple search queries. It detects binary entrypoints (`cmd/` directories, root `main.go`, goreleaser configs) and stores them in a SQLite database with metadata (stars, description, version).

Run it locally:

```bash
GITHUB_TOKEN=ghp_... python generate_tools_json.py
```

### Build verification (`gomanager verify`)

Attempts `go install` on unverified packages and updates their build status. If a build fails, it retries with `CGO_ENABLED=0`. Each binary gets a status:

| Status      | Meaning                              |
| ----------- | ------------------------------------ |
| `confirmed` | Successfully built with `go install` |
| `failed`    | Build failed (error recorded)        |
| `regressed` | Build failed (previously confirmed)  |
| `unknown`   | Not yet tested                       |
| `pending`   | Queued for verification              |

Run locally against the repo database:

```bash
gomanager verify -d ./database.db -n 20
gomanager verify -d ./database.db --reverify   # also retry failed packages
```

### Web frontend (`index.html`)

A static single-page app that loads `database.db` with sql.js. Features search, filtering by build status, sortable columns, copy-to-clipboard install commands, and dark mode. Host it with GitHub Pages or any static file server.

To preview locally:

```bash
./serve
```

This starts a local HTTP server on the nearest available port (starting at 8000) and opens it in your browser.

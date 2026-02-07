#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "requests",
# ]
# ///
from __future__ import annotations

import json
import os
import sqlite3
import time
from pathlib import Path
from typing import Any

import requests

TOOLS_DB = "database.db"
SCANNED_REPOS_FILE = "scanned_repos.json"

# Multiple search queries to broaden discovery
SEARCH_QUERIES = [
    "topic:go+topic:cli",
    "topic:golang+topic:cli",
    "topic:go+topic:tool",
    "topic:golang+topic:tool",
    "topic:go+topic:devtools",
    "language:go+topic:cli",
    "language:go+topic:command-line",
]

# Max pages to fetch per query (100 results per page)
MAX_PAGES_PER_QUERY = 3
RESULTS_PER_PAGE = 100

# Rate limit safety margin
RATE_LIMIT_BUFFER = 10


def get_headers() -> dict[str, str]:
    github_token = os.getenv("GITHUB_TOKEN", "")
    return {"Authorization": f"token {github_token}"} if github_token else {}


def check_rate_limit(headers: dict[str, str]) -> None:
    """Check GitHub API rate limit and sleep if we're running low."""
    url = "https://api.github.com/rate_limit"
    r = requests.get(url, headers=headers)
    if r.status_code != 200:
        return
    data = r.json()
    remaining = data.get("resources", {}).get("core", {}).get("remaining", 999)
    reset_time = data.get("resources", {}).get("core", {}).get("reset", 0)
    if remaining < RATE_LIMIT_BUFFER:
        wait = max(reset_time - int(time.time()), 0) + 5
        print(f"Rate limit low ({remaining} remaining). Sleeping {wait}s...")
        time.sleep(wait)


def api_get(url: str, headers: dict[str, str]) -> requests.Response:
    """Make a GET request with rate-limit awareness."""
    r = requests.get(url, headers=headers)
    remaining = int(r.headers.get("X-RateLimit-Remaining", "999"))
    if remaining < RATE_LIMIT_BUFFER:
        reset_time = int(r.headers.get("X-RateLimit-Reset", "0"))
        wait = max(reset_time - int(time.time()), 0) + 5
        print(f"Rate limit low ({remaining} remaining). Sleeping {wait}s...")
        time.sleep(wait)
    return r


def search_go_tools(headers: dict[str, str], scanned_repos: set[str]) -> list[dict[str, Any]]:
    """Search GitHub with multiple queries and pagination. Deduplicates results."""
    seen_ids: set[int] = set()
    all_repos: list[dict[str, Any]] = []

    for query in SEARCH_QUERIES:
        for page in range(1, MAX_PAGES_PER_QUERY + 1):
            url = (
                f"https://api.github.com/search/repositories"
                f"?q={query}&sort=stars&order=desc"
                f"&per_page={RESULTS_PER_PAGE}&page={page}"
            )
            r = api_get(url, headers)
            if r.status_code != 200:
                print(f"Search failed for query={query} page={page}: {r.status_code}")
                break

            items = r.json().get("items", [])
            if not items:
                break

            for item in items:
                repo_key = f"{item['owner']['login']}/{item['name']}"
                if item["id"] not in seen_ids and repo_key not in scanned_repos:
                    seen_ids.add(item["id"])
                    all_repos.append(item)

            # Respect search API rate limit (30 req/min unauthenticated)
            time.sleep(2)

        print(f"Query '{query}': collected {len(all_repos)} unique new repos so far")

    return all_repos


def check_file_exists(owner: str, repo: str, path: str, headers: dict[str, str]) -> bool:
    """Check if a file exists in the repository."""
    url = f"https://api.github.com/repos/{owner}/{repo}/contents/{path}"
    r = api_get(url, headers)
    return r.status_code == 200


def has_goreleaser_config(owner: str, repo: str, headers: dict[str, str]) -> bool:
    """Check if the repo has a goreleaser configuration (strong signal it produces binaries)."""
    for path in [".goreleaser.yml", ".goreleaser.yaml", "goreleaser.yml", "goreleaser.yaml"]:
        if check_file_exists(owner, repo, path, headers):
            return True
    return False


def find_cli_entrypoints(
    owner: str, repo: str, repo_name: str, headers: dict[str, str]
) -> list[tuple[str, str]]:
    """
    Find CLI entrypoints in a Go repository using the Contents API.
    Returns list of tuples: (binary_name, package_path_suffix)
    - For cmd/foo: ("foo", "cmd/foo")
    - For root main.go: (repo_name, "")
    """
    entrypoints: list[tuple[str, str]] = []

    # First, check for cmd/ directory (standard Go layout)
    url = f"https://api.github.com/repos/{owner}/{repo}/contents/cmd"
    r = api_get(url, headers)
    if r.status_code == 200:
        data = r.json()
        if isinstance(data, list):
            cmd_dirs = [item["name"] for item in data if item["type"] == "dir"]
            for cmd in cmd_dirs:
                entrypoints.append((cmd, f"cmd/{cmd}"))
            if entrypoints:
                return entrypoints

    # If no cmd/ directory, check for root-level main.go
    if check_file_exists(owner, repo, "main.go", headers):
        entrypoints.append((repo_name, ""))
        return entrypoints

    # Check if goreleaser exists (implies binaries even if we can't find entrypoints easily)
    if has_goreleaser_config(owner, repo, headers):
        # Assume root package is the binary
        entrypoints.append((repo_name, ""))
        return entrypoints

    return entrypoints


def get_latest_release(owner: str, repo: str, headers: dict[str, str]) -> str:
    url = f"https://api.github.com/repos/{owner}/{repo}/releases/latest"
    r = api_get(url, headers)
    if r.status_code != 200:
        return "latest"
    return r.json().get("tag_name", "latest")


# ---------------------------------------------------------------------------
# Database helpers
# ---------------------------------------------------------------------------

def init_database(db_path: str) -> sqlite3.Connection:
    """Initialize (or recreate) the SQLite database with the enriched schema."""
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS binaries (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            package TEXT NOT NULL UNIQUE,
            version TEXT,
            description TEXT,
            repo_url TEXT,
            stars INTEGER DEFAULT 0,
            build_status TEXT DEFAULT 'unknown'
                CHECK(build_status IN ('unknown','confirmed','failed','pending')),
            build_flags TEXT DEFAULT '{}',
            build_error TEXT,
            last_verified TIMESTAMP,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        )
    """)

    cursor.execute("CREATE INDEX IF NOT EXISTS idx_package ON binaries(package)")
    cursor.execute("CREATE INDEX IF NOT EXISTS idx_name ON binaries(name)")
    cursor.execute("CREATE INDEX IF NOT EXISTS idx_build_status ON binaries(build_status)")
    cursor.execute("CREATE INDEX IF NOT EXISTS idx_stars ON binaries(stars)")

    conn.commit()
    return conn


def get_existing_packages(conn: sqlite3.Connection) -> set[str]:
    """Get all existing package paths from the database."""
    cursor = conn.cursor()
    cursor.execute("SELECT package FROM binaries")
    return {row[0] for row in cursor.fetchall()}


def upsert_binary(
    conn: sqlite3.Connection,
    *,
    name: str,
    package: str,
    version: str,
    description: str,
    repo_url: str,
    stars: int,
) -> bool:
    """Insert or update a binary in the database. Returns True if a new row was inserted."""
    cursor = conn.cursor()
    cursor.execute(
        """
        INSERT INTO binaries (name, package, version, description, repo_url, stars)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(package) DO UPDATE SET
            version = excluded.version,
            description = excluded.description,
            repo_url = excluded.repo_url,
            stars = excluded.stars,
            updated_at = CURRENT_TIMESTAMP
        """,
        (name, package, version, description, repo_url, stars),
    )
    conn.commit()
    return cursor.lastrowid is not None and cursor.rowcount == 1


def load_scanned_repos(repos_file: str) -> set[str]:
    """Load scanned repositories from JSON file."""
    if Path(repos_file).exists():
        with open(repos_file) as f:
            return set(json.load(f))
    return set()


def save_scanned_repos(repos_file: str, repos: set[str]) -> None:
    """Save scanned repositories to JSON file."""
    with open(repos_file, "w") as f:
        json.dump(sorted(repos), f, indent=2)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    headers = get_headers()
    check_rate_limit(headers)

    # Initialize database (creates table if not exists)
    conn = init_database(TOOLS_DB)

    # Load existing data
    existing_packages = get_existing_packages(conn)
    scanned_repos = load_scanned_repos(SCANNED_REPOS_FILE)

    # Discover new tools
    repos = search_go_tools(headers, scanned_repos)
    new_binaries_count = 0

    print(f"Processing {len(repos)} new repositories...")

    for i, repo in enumerate(repos):
        owner = repo["owner"]["login"]
        repo_name = repo["name"]
        repo_key = f"{owner}/{repo_name}"
        description = repo.get("description") or ""
        stars = repo.get("stargazers_count", 0)
        repo_url = repo.get("html_url", f"https://github.com/{repo_key}")

        print(f"[{i + 1}/{len(repos)}] Scanning {repo_key} ({stars} stars)...")

        entrypoints = find_cli_entrypoints(owner, repo_name, repo_name, headers)
        if entrypoints:
            version = get_latest_release(owner, repo_name, headers)
            for binary_name, package_suffix in entrypoints:
                if package_suffix:
                    package_path = f"github.com/{owner}/{repo_name}/{package_suffix}"
                else:
                    package_path = f"github.com/{owner}/{repo_name}"

                if package_path not in existing_packages:
                    upsert_binary(
                        conn,
                        name=binary_name,
                        package=package_path,
                        version=version,
                        description=description,
                        repo_url=repo_url,
                        stars=stars,
                    )
                    existing_packages.add(package_path)
                    new_binaries_count += 1
        else:
            print(f"  No binaries found in {repo_key}")

        scanned_repos.add(repo_key)

    # Save scanned repos to JSON
    save_scanned_repos(SCANNED_REPOS_FILE, scanned_repos)

    conn.close()
    print(f"Done. Added {new_binaries_count} new binaries.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

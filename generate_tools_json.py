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
from pathlib import Path
import sqlite3
from typing import Any

import requests

TOOLS_DB = "database.db"
SCANNED_REPOS_FILE = "scanned_repos.json"


def get_headers() -> dict[str, str]:
    github_token = os.getenv("GITHUB_TOKEN", "")
    return {"Authorization": f"token {github_token}"} if github_token else {}


def search_go_tools(headers: dict[str, str]) -> list[dict[str, Any]]:
    url = "https://api.github.com/search/repositories?q=topic:go+topic:cli&sort=stars&order=desc&per_page=10"
    r = requests.get(url, headers=headers)
    return r.json().get("items", [])


def check_file_exists(owner: str, repo: str, path: str, headers: dict[str, str]) -> bool:
    """Check if a file exists in the repository."""
    url = f"https://api.github.com/repos/{owner}/{repo}/contents/{path}"
    r = requests.get(url, headers=headers)
    return r.status_code == 200


def find_cli_entrypoints(
    owner: str, repo: str, repo_name: str, headers: dict[str, str]
) -> list[tuple[str, str]]:
    """
    Find CLI entrypoints in a Go repository using the Contents API.
    Returns list of tuples: (binary_name, package_path_suffix)
    - For cmd/foo: ("foo", "cmd/foo")
    - For root main.go: (repo_name, "")  # empty suffix means root package
    """
    entrypoints: list[tuple[str, str]] = []

    # First, check for cmd/ directory (standard Go layout)
    url = f"https://api.github.com/repos/{owner}/{repo}/contents/cmd"
    r = requests.get(url, headers=headers)
    if r.status_code == 200:
        cmd_dirs = [item["name"] for item in r.json() if item["type"] == "dir"]
        for cmd in cmd_dirs:
            entrypoints.append((cmd, f"cmd/{cmd}"))
        return entrypoints

    # If no cmd/ directory, check for root-level main.go
    if check_file_exists(owner, repo, "main.go", headers):
        entrypoints.append((repo_name, ""))
        return entrypoints

    # Check other common locations for main.go
    # Some repos put main.go in pkg/ or internal/ directories
    common_paths = ["pkg/main.go", "internal/main.go"]
    for path in common_paths:
        if check_file_exists(owner, repo, path, headers):
            # Use repo name as binary name, package path is just the directory
            package_suffix = path.split("/")[0]  # "pkg" or "internal"
            entrypoints.append((repo_name, package_suffix))
            break

    return entrypoints


def get_latest_release(owner: str, repo: str, headers: dict[str, str]) -> str:
    url = f"https://api.github.com/repos/{owner}/{repo}/releases/latest"
    r = requests.get(url, headers=headers)
    if r.status_code != 200:
        return "latest"
    return r.json().get("tag_name", "latest")


def init_database(db_path: str) -> None:
    """Initialize the SQLite database with required tables."""
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS binaries (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            package TEXT NOT NULL UNIQUE,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        )
    """)

    cursor.execute("""
        CREATE INDEX IF NOT EXISTS idx_package ON binaries(package)
    """)

    cursor.execute("""
        CREATE INDEX IF NOT EXISTS idx_name ON binaries(name)
    """)

    conn.commit()
    conn.close()


def get_existing_packages(db_path: str) -> set[str]:
    """Get all existing package paths from the database."""
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()
    cursor.execute("SELECT package FROM binaries")
    packages = {row[0] for row in cursor.fetchall()}
    conn.close()
    return packages


def add_binary(db_path: str, name: str, package: str) -> None:
    """Add a binary to the database."""
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()
    cursor.execute("INSERT OR IGNORE INTO binaries (name, package) VALUES (?, ?)", (name, package))
    conn.commit()
    conn.close()


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


def main() -> int:
    headers = get_headers()

    # Initialize database
    init_database(TOOLS_DB)

    # Load existing data
    existing_packages = get_existing_packages(TOOLS_DB)
    scanned_repos = load_scanned_repos(SCANNED_REPOS_FILE)

    # Discover new tools
    repos = search_go_tools(headers)
    new_binaries_count = 0

    for repo in repos:
        owner = repo["owner"]["login"]
        repo_name = repo["name"]
        repo_key = f"{owner}/{repo_name}"

        if repo_key in scanned_repos:
            print(f"Skipping already scanned repo: {repo_key}")
            continue

        entrypoints = find_cli_entrypoints(owner, repo_name, repo_name, headers)
        if entrypoints:
            version = get_latest_release(owner, repo_name, headers)
            for binary_name, package_suffix in entrypoints:
                if package_suffix:
                    package_path = f"github.com/{owner}/{repo_name}/{package_suffix}@{version}"
                else:
                    # Root-level main.go - package is at repo root
                    package_path = f"github.com/{owner}/{repo_name}@{version}"

                if package_path not in existing_packages:
                    add_binary(TOOLS_DB, binary_name, package_path)
                    existing_packages.add(package_path)
                    new_binaries_count += 1
        else:
            print(f"No binaries found in {repo_key}")

        scanned_repos.add(repo_key)

    # Save scanned repos to JSON
    save_scanned_repos(SCANNED_REPOS_FILE, scanned_repos)

    print(f"Added {new_binaries_count} new binaries.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

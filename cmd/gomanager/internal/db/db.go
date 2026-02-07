package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Binary represents a row from the binaries table.
type Binary struct {
	ID          int
	Name        string
	Package     string
	Version     string
	Description string
	RepoURL     string
	Stars       int
	BuildStatus string
	BuildFlags  string
	BuildError  string
}

// DBPath returns the path to the local database file.
func DBPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	dir := filepath.Join(configDir, "gomanager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create config directory: %w", err)
	}
	return filepath.Join(dir, "database.db"), nil
}

// Open opens the local database for reading.
func Open() (*sql.DB, error) {
	path, err := DBPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("database not found at %s — run 'gomanager update-db' first", path)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}
	return conn, nil
}

// Search finds binaries matching a query string.
func Search(conn *sql.DB, query string) ([]Binary, error) {
	q := "%" + strings.ToLower(query) + "%"
	rows, err := conn.Query(
		`SELECT id, name, package, COALESCE(version,'latest'),
		        COALESCE(description,''), COALESCE(repo_url,''),
		        COALESCE(stars,0), COALESCE(build_status,'unknown'),
		        COALESCE(build_flags,'{}'), COALESCE(build_error,'')
		 FROM binaries
		 WHERE LOWER(name) LIKE ? OR LOWER(package) LIKE ? OR LOWER(description) LIKE ?
		 ORDER BY stars DESC`,
		q, q, q,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// GetByName finds a binary by exact name.
func GetByName(conn *sql.DB, name string) (*Binary, error) {
	row := conn.QueryRow(
		`SELECT id, name, package, COALESCE(version,'latest'),
		        COALESCE(description,''), COALESCE(repo_url,''),
		        COALESCE(stars,0), COALESCE(build_status,'unknown'),
		        COALESCE(build_flags,'{}'), COALESCE(build_error,'')
		 FROM binaries WHERE LOWER(name) = LOWER(?)
		 ORDER BY stars DESC LIMIT 1`,
		name,
	)
	var b Binary
	err := row.Scan(&b.ID, &b.Name, &b.Package, &b.Version,
		&b.Description, &b.RepoURL, &b.Stars, &b.BuildStatus,
		&b.BuildFlags, &b.BuildError)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("binary %q not found in database", name)
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListAll returns all binaries ordered by stars descending.
func ListAll(conn *sql.DB) ([]Binary, error) {
	rows, err := conn.Query(
		`SELECT id, name, package, COALESCE(version,'latest'),
		        COALESCE(description,''), COALESCE(repo_url,''),
		        COALESCE(stars,0), COALESCE(build_status,'unknown'),
		        COALESCE(build_flags,'{}'), COALESCE(build_error,'')
		 FROM binaries ORDER BY stars DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

func scanBinaries(rows *sql.Rows) ([]Binary, error) {
	var result []Binary
	for rows.Next() {
		var b Binary
		if err := rows.Scan(&b.ID, &b.Name, &b.Package, &b.Version,
			&b.Description, &b.RepoURL, &b.Stars, &b.BuildStatus,
			&b.BuildFlags, &b.BuildError); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// InstallCommand returns the full install command string for a binary,
// including any required environment flags.
func (b *Binary) InstallCommand() string {
	version := b.Version
	if version == "" {
		version = "latest"
	}
	cmd := fmt.Sprintf("go install %s@%s", b.Package, version)
	flags := b.EnvFlags()
	if flags != "" {
		cmd = flags + " " + cmd
	}
	return cmd
}

// EnvFlags returns the environment variable prefix (e.g. "CGO_ENABLED=0")
// parsed from the BuildFlags JSON field.
func (b *Binary) EnvFlags() string {
	if b.BuildFlags == "" || b.BuildFlags == "{}" {
		return ""
	}
	// Simple JSON parsing without importing encoding/json to keep it light
	// BuildFlags format: {"KEY":"VALUE",...}
	s := strings.Trim(b.BuildFlags, "{}")
	if s == "" {
		return ""
	}
	var parts []string
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.Trim(kv[0], `"`)
		val := strings.Trim(kv[1], `"`)
		parts = append(parts, key+"="+val)
	}
	return strings.Join(parts, " ")
}


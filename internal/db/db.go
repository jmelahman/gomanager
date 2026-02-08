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
	IsPrimary   bool
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
	return OpenPath(path)
}

// OpenPath opens a database at the given path.
func OpenPath(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("database not found at %s", path)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}
	return conn, nil
}

// CreatePath creates (if needed) and opens a database at the given path.
// Unlike OpenPath, this does not error if the file does not exist yet.
func CreatePath(path string) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}
	return conn, nil
}

// InitSchema creates the binaries table and indexes if they don't exist.
func InitSchema(conn *sql.DB) error {
	_, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS binaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			package TEXT NOT NULL UNIQUE,
			version TEXT,
			description TEXT,
			repo_url TEXT,
			stars INTEGER DEFAULT 0,
			is_primary INTEGER DEFAULT 1,
			build_status TEXT DEFAULT 'unknown'
				CHECK(build_status IN ('unknown','confirmed','failed','pending','regressed')),
			build_flags TEXT DEFAULT '{}',
			build_error TEXT,
			last_verified TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}
	for _, idx := range []string{
		"CREATE INDEX IF NOT EXISTS idx_package ON binaries(package)",
		"CREATE INDEX IF NOT EXISTS idx_name ON binaries(name)",
		"CREATE INDEX IF NOT EXISTS idx_build_status ON binaries(build_status)",
		"CREATE INDEX IF NOT EXISTS idx_stars ON binaries(stars)",
	} {
		if _, err := conn.Exec(idx); err != nil {
			return err
		}
	}
	return nil
}

// UpsertBinary inserts or updates a binary. On conflict (package), is_primary
// is preserved so manual curation is not overwritten by the scanner.
func UpsertBinary(conn *sql.DB, name, pkg, version, description, repoURL string, stars int, isPrimary bool) error {
	primary := 0
	if isPrimary {
		primary = 1
	}
	_, err := conn.Exec(`
		INSERT INTO binaries (name, package, version, description, repo_url, stars, is_primary)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(package) DO UPDATE SET
			version = excluded.version,
			description = excluded.description,
			repo_url = excluded.repo_url,
			stars = excluded.stars,
			updated_at = CURRENT_TIMESTAMP
	`, name, pkg, version, description, repoURL, stars, primary)
	return err
}

// GetExistingPackages returns all package paths currently in the database.
func GetExistingPackages(conn *sql.DB) (map[string]bool, error) {
	rows, err := conn.Query("SELECT package FROM binaries")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var pkg string
		if err := rows.Scan(&pkg); err != nil {
			return nil, err
		}
		result[pkg] = true
	}
	return result, rows.Err()
}

// selectCols is the standard column list for binary queries.
const selectCols = `id, name, package, COALESCE(version,'latest'),
        COALESCE(description,''), COALESCE(repo_url,''),
        COALESCE(stars,0), COALESCE(is_primary,1),
        COALESCE(build_status,'unknown'),
        COALESCE(build_flags,'{}'), COALESCE(build_error,'')`

// GetUnverified returns binaries that need build verification.
func GetUnverified(conn *sql.DB, statuses []string, limit int) ([]Binary, error) {
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, s := range statuses {
		placeholders[i] = "?"
		args[i] = s
	}
	args = append(args, limit)

	query := fmt.Sprintf(
		`SELECT %s FROM binaries
		 WHERE build_status IN (%s)
		 ORDER BY stars DESC
		 LIMIT ?`,
		selectCols, strings.Join(placeholders, ","),
	)

	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// UpdateBuildResult updates the build status for a binary after verification.
func UpdateBuildResult(conn *sql.DB, id int, status string, flags string, buildErr string) error {
	_, err := conn.Exec(
		`UPDATE binaries SET
			build_status = ?,
			build_flags = ?,
			build_error = ?,
			last_verified = datetime('now')
		 WHERE id = ?`,
		status, flags, buildErr, id,
	)
	return err
}

// Search finds binaries matching a query string.
func Search(conn *sql.DB, query string) ([]Binary, error) {
	q := "%" + strings.ToLower(query) + "%"
	rows, err := conn.Query(
		fmt.Sprintf(
			`SELECT %s FROM binaries
			 WHERE LOWER(name) LIKE ? OR LOWER(package) LIKE ? OR LOWER(description) LIKE ?
			 ORDER BY stars DESC`, selectCols),
		q, q, q,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// GetByName finds a binary by exact name. If multiple packages share the
// same name, the one with the most stars is returned.
func GetByName(conn *sql.DB, name string) (*Binary, error) {
	row := conn.QueryRow(
		fmt.Sprintf(
			`SELECT %s FROM binaries WHERE LOWER(name) = LOWER(?)
			 ORDER BY stars DESC LIMIT 1`, selectCols),
		name,
	)
	b, err := scanBinary(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("binary %q not found in database", name)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// FindByName returns all binaries matching the given name (case-insensitive).
func FindByName(conn *sql.DB, name string) ([]Binary, error) {
	rows, err := conn.Query(
		fmt.Sprintf(
			`SELECT %s FROM binaries WHERE LOWER(name) = LOWER(?)
			 ORDER BY stars DESC`, selectCols),
		name,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// GetByPackage finds a binary by exact package path.
func GetByPackage(conn *sql.DB, pkg string) (*Binary, error) {
	row := conn.QueryRow(
		fmt.Sprintf(
			`SELECT %s FROM binaries WHERE package = ?
			 LIMIT 1`, selectCols),
		pkg,
	)
	b, err := scanBinary(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("package %q not found in database", pkg)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ListAll returns all binaries ordered by stars descending.
func ListAll(conn *sql.DB) ([]Binary, error) {
	rows, err := conn.Query(
		fmt.Sprintf(`SELECT %s FROM binaries ORDER BY stars DESC`, selectCols),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

func scanBinary(row *sql.Row) (*Binary, error) {
	var b Binary
	var isPrimary int
	err := row.Scan(&b.ID, &b.Name, &b.Package, &b.Version,
		&b.Description, &b.RepoURL, &b.Stars, &isPrimary,
		&b.BuildStatus, &b.BuildFlags, &b.BuildError)
	b.IsPrimary = isPrimary != 0
	return &b, err
}

func scanBinaries(rows *sql.Rows) ([]Binary, error) {
	var result []Binary
	for rows.Next() {
		var b Binary
		var isPrimary int
		if err := rows.Scan(&b.ID, &b.Name, &b.Package, &b.Version,
			&b.Description, &b.RepoURL, &b.Stars, &isPrimary,
			&b.BuildStatus, &b.BuildFlags, &b.BuildError); err != nil {
			return nil, err
		}
		b.IsPrimary = isPrimary != 0
		result = append(result, b)
	}
	return result, rows.Err()
}

// MigrateSchema updates the database schema to support the 'regressed' build status.
func MigrateSchema(conn *sql.DB) error {
	var tableSql string
	err := conn.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='binaries'").Scan(&tableSql)
	if err != nil {
		return nil // table doesn't exist, nothing to migrate
	}
	if !strings.Contains(tableSql, "CHECK") || strings.Contains(tableSql, "'regressed'") {
		return nil // already has regressed or no CHECK constraint
	}

	newSql := strings.Replace(tableSql,
		"'unknown','confirmed','failed','pending'",
		"'unknown','confirmed','failed','pending','regressed'", 1)

	if _, err := conn.Exec("PRAGMA writable_schema = ON"); err != nil {
		return fmt.Errorf("enable writable_schema: %w", err)
	}
	if _, err := conn.Exec("UPDATE sqlite_master SET sql = ? WHERE type='table' AND name='binaries'", newSql); err != nil {
		conn.Exec("PRAGMA writable_schema = OFF")
		return fmt.Errorf("update schema: %w", err)
	}
	if _, err := conn.Exec("PRAGMA writable_schema = OFF"); err != nil {
		return fmt.Errorf("disable writable_schema: %w", err)
	}
	return nil
}

// UpdateVersion updates the version for a specific package.
func UpdateVersion(conn *sql.DB, id int, newVersion string) error {
	_, err := conn.Exec(
		`UPDATE binaries SET version = ?, updated_at = datetime('now') WHERE id = ?`,
		newVersion, id,
	)
	return err
}

// GetStaleConfirmed returns confirmed packages that were updated since their last verification.
// These are packages whose version was bumped by update-versions and need re-testing.
func GetStaleConfirmed(conn *sql.DB, limit int) ([]Binary, error) {
	rows, err := conn.Query(
		fmt.Sprintf(`SELECT %s FROM binaries
		 WHERE build_status = 'confirmed'
		   AND updated_at > COALESCE(last_verified, '1970-01-01')
		 ORDER BY stars DESC LIMIT ?`, selectCols),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// PackageExists checks if a package path already exists in the database.
func PackageExists(conn *sql.DB, pkg string) (bool, error) {
	var count int
	err := conn.QueryRow("SELECT COUNT(*) FROM binaries WHERE package = ?", pkg).Scan(&count)
	return count > 0, err
}

// GetReposWithoutRoot returns repos that have cmd/ entries but no root-level entry.
// Returns a list of binaries representing one entry per repo (to get version/metadata).
func GetReposWithoutRoot(conn *sql.DB, limit int) ([]Binary, error) {
	// Find repos where we have cmd/ subpackages but no root package.
	// The root package is "github.com/owner/repo" (exactly 3 path segments).
	// cmd/ packages are "github.com/owner/repo/cmd/..." (more than 3 segments).
	rows, err := conn.Query(
		fmt.Sprintf(`SELECT %s FROM binaries b1
		 WHERE package LIKE '%%/cmd/%%'
		   AND NOT EXISTS (
		     SELECT 1 FROM binaries b2
		     WHERE b2.package = SUBSTR(b1.package, 1, INSTR(SUBSTR(b1.package, 12), '/') + 10)
		   )
		 GROUP BY SUBSTR(package, 1, INSTR(SUBSTR(package, 12), '/') + 10)
		 ORDER BY stars DESC
		 LIMIT ?`, selectCols),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBinaries(rows)
}

// InsertBinary inserts a new binary entry into the database.
func InsertBinary(conn *sql.DB, name, pkg, version, description, repoURL string, stars int, isPrimary bool, buildStatus, buildFlags string) error {
	primary := 0
	if isPrimary {
		primary = 1
	}
	_, err := conn.Exec(
		`INSERT OR IGNORE INTO binaries (name, package, version, description, repo_url, stars, is_primary, build_status, build_flags, last_verified)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		name, pkg, version, description, repoURL, stars, primary, buildStatus, buildFlags,
	)
	return err
}

// UpdatePackagePath updates the package path for a binary.
// Used to fix v2+ module paths discovered from go.mod.
func UpdatePackagePath(conn *sql.DB, id int, newPkg string) error {
	_, err := conn.Exec(
		`UPDATE binaries SET package = ?, updated_at = datetime('now') WHERE id = ?`,
		newPkg, id,
	)
	return err
}

// DeleteBinary removes a binary entry by ID.
func DeleteBinary(conn *sql.DB, id int) error {
	_, err := conn.Exec(`DELETE FROM binaries WHERE id = ?`, id)
	return err
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

// allowedBuildEnv is the set of environment variable names that may be set
// from the database build_flags field. Anything outside this list is ignored
// to prevent a compromised database from injecting dangerous variables like
// LD_PRELOAD or PATH.
var allowedBuildEnv = map[string]bool{
	"CGO_ENABLED": true,
	"GOOS":        true,
	"GOARCH":      true,
	"GOARM":       true,
	"CC":          true,
	"CXX":         true,
	"CGO_CFLAGS":  true,
	"CGO_LDFLAGS": true,
}

// EnvFlags returns the environment variable prefix (e.g. "CGO_ENABLED=0")
// parsed from the BuildFlags JSON field. Only allowlisted variable names
// are included; unknown keys are silently dropped.
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
		if !allowedBuildEnv[key] {
			continue
		}
		parts = append(parts, key+"="+val)
	}
	return strings.Join(parts, " ")
}

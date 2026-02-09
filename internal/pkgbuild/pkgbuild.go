package pkgbuild

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/template"

	"github.com/jmelahman/gomanager/internal/db"
)

// safeName matches valid PKGBUILD pkgname values (alphanumerics, hyphens, dots, underscores).
var safeName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// safePackage matches valid Go module paths (alphanumerics, dots, slashes, hyphens, underscores).
var safePackage = regexp.MustCompile(`^[a-zA-Z0-9./_-]+$`)

const pkgbuildTemplate = `# Maintainer: gomanager <gomanager@generated>
pkgname={{.PkgName}}
pkgver={{.PkgVer}}
pkgrel=1
pkgdesc="{{.PkgDesc}}"
arch=('x86_64' 'aarch64')
url="{{.URL}}"
license=('{{.LicenseID}}')
{{- if .NoCGO}}
depends=()
{{- else}}
depends=('glibc')
{{- end}}
makedepends=('go' 'git')
source=("git+{{.GitURL}}.git#tag={{.TagPrefix}}$pkgver")
sha256sums=('SKIP')

build() {
  cd "$pkgname" || exit
{{- range .EnvVars}}
  export {{.}}
{{- end}}
{{- if not .HasGoMod}}
  go mod init {{.ModulePath}}
  go mod tidy
{{- end}}
  go build \
    -trimpath \
{{- if .HasGoMod}}
    -mod=readonly \
    -modcacherw \
{{- end}}
    -ldflags='-s -w' \
    -o {{.BuildPath}}/$pkgname \
    {{.BuildPath}}
}

package() {
  cd "$pkgname" || exit
  install -Dm 755 {{.BuildPath}}/$pkgname -t "$pkgdir/usr/bin"
{{- if .LicenseFile}}
  install -Dm 644 {{.LicenseFile}} -t "$pkgdir/usr/share/licenses/$pkgname"
{{- end}}
{{- if .ReadmeFile}}
  install -Dm 644 {{.ReadmeFile}} -t "$pkgdir/usr/share/doc/$pkgname"
{{- end}}
}
`

// Options holds optional metadata that can be discovered from the repository
// prior to PKGBUILD generation (e.g. via the GitHub API).
type Options struct {
	// LicenseID is the SPDX license identifier (e.g. "MIT", "Apache-2.0").
	// If empty, "unknown" is used.
	LicenseID string
	// LicenseFile is the exact filename of the license (e.g. "LICENSE", "LICENSE.md").
	// If empty, no license install line is emitted.
	LicenseFile string
	// ReadmeFile is the exact filename of the readme (e.g. "README.md", "README").
	// If empty, no readme install line is emitted.
	ReadmeFile string
	// HasGoMod indicates whether the repository has a go.mod file.
	// When true, -mod=readonly and -modcacherw flags are included in the build.
	HasGoMod bool
}

// TemplateData holds the values for PKGBUILD generation.
type TemplateData struct {
	PkgName     string
	PkgVer      string
	PkgDesc     string
	URL         string
	GitURL      string
	TagPrefix   string
	BuildPath   string
	ModulePath  string
	EnvVars     []string
	NoCGO       bool
	HasGoMod    bool
	LicenseID   string
	LicenseFile string
	ReadmeFile  string
}

// Generate writes a PKGBUILD to the given writer for the specified binary.
// If opts is nil, license and readme install lines are omitted.
func Generate(w io.Writer, b *db.Binary, opts *Options) error {
	version := b.Version
	if version == "" || version == "latest" {
		return fmt.Errorf("cannot generate PKGBUILD for %q: no version tag available (version is %q)", b.Name, version)
	}
	// Strip leading 'v' from version for PKGBUILD convention
	pkgVer := strings.TrimPrefix(version, "v")

	// Validate fields that are interpolated into shell context
	if !safeName.MatchString(b.Name) {
		return fmt.Errorf("unsafe package name %q for PKGBUILD generation", b.Name)
	}
	if !safePackage.MatchString(b.Package) {
		return fmt.Errorf("unsafe package path %q for PKGBUILD generation", b.Package)
	}

	desc := b.Description
	// Escape double quotes in description for the PKGBUILD shell context
	desc = strings.ReplaceAll(desc, `"`, `\"`)
	if desc == "" {
		desc = fmt.Sprintf("Go binary: %s", b.Name)
	}

	url := b.RepoURL
	if url == "" {
		url = "https://" + b.Package
	}
	// Git source URL: strip trailing .git if present, we add it in the template
	gitURL := strings.TrimSuffix(url, ".git")

	// Determine tag prefix: if the original version started with 'v', use 'v'
	tagPrefix := ""
	if strings.HasPrefix(version, "v") {
		tagPrefix = "v"
	}

	// Determine the module path (e.g. "github.com/owner/repo" or
	// "github.com/owner/repo/v4") and the build path relative to the repo root.
	// For root packages (github.com/owner/repo), buildPath is "."
	// For sub-packages (github.com/owner/repo/cmd/foo), buildPath is "./cmd/foo"
	buildPath := "."
	modulePath := strings.Join(strings.SplitN(b.Package, "/", 4)[:3], "/")
	parts := strings.SplitN(b.Package, "/", 4) // github.com / owner / repo / rest
	if len(parts) == 4 {
		sub := parts[3]
		// If the sub-path is just a major version (e.g. "v4"), include it in modulePath
		if regexp.MustCompile(`^v\d+$`).MatchString(sub) {
			modulePath = b.Package
		} else {
			// Strip leading version prefix if present (e.g. "v4/cmd/foo" -> "cmd/foo")
			if idx := strings.Index(sub, "/"); idx >= 0 {
				prefix := sub[:idx]
				if regexp.MustCompile(`^v\d+$`).MatchString(prefix) {
					modulePath = modulePath + "/" + prefix
					sub = sub[idx+1:]
				}
			}
			buildPath = "./" + sub
		}
	}

	var envVars []string
	flags := b.EnvFlags()
	if flags != "" {
		for _, f := range strings.Split(flags, " ") {
			envVars = append(envVars, f)
		}
	}

	// Detect if CGO is explicitly disabled
	noCGO := false
	for _, e := range envVars {
		if e == "CGO_ENABLED=0" {
			noCGO = true
			break
		}
	}

	var licenseID, licenseFile, readmeFile string
	hasGoMod := true // assume modern project if opts not available
	if opts != nil {
		licenseID = opts.LicenseID
		licenseFile = opts.LicenseFile
		readmeFile = opts.ReadmeFile
		hasGoMod = opts.HasGoMod
	}
	if licenseID == "" {
		licenseID = "unknown"
	}

	data := TemplateData{
		PkgName:     b.Name,
		PkgVer:      pkgVer,
		PkgDesc:     desc,
		URL:         url,
		GitURL:      gitURL,
		TagPrefix:   tagPrefix,
		BuildPath:   buildPath,
		ModulePath:  modulePath,
		EnvVars:     envVars,
		NoCGO:       noCGO,
		HasGoMod:    hasGoMod,
		LicenseID:   licenseID,
		LicenseFile: licenseFile,
		ReadmeFile:  readmeFile,
	}

	tmpl, err := template.New("PKGBUILD").Parse(pkgbuildTemplate)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}
	return tmpl.Execute(w, data)
}

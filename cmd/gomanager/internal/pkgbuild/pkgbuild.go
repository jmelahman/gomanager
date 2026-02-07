package pkgbuild

import (
	"fmt"
	"strings"
	"text/template"
	"io"

	"github.com/jamison/gomanager/cmd/gomanager/internal/db"
)

const pkgbuildTemplate = `# Maintainer: gomanager <gomanager@generated>
pkgname={{.PkgName}}
pkgver={{.PkgVer}}
pkgrel=1
pkgdesc='{{.PkgDesc}}'
arch=('x86_64' 'aarch64')
url='{{.URL}}'
license=('unknown')
makedepends=('go')
source=()
sha256sums=()

build() {
  cd "$srcdir"
  {{- range .EnvVars}}
  export {{.}}
  {{- end}}
  go install {{.Package}}@v${pkgver}
  # Binary is installed to ~/go/bin by default; we build it explicitly instead
  go build -o "${pkgname}" -trimpath -ldflags='-s -w' {{.Package}}@v${pkgver} || \
    go build -o "${pkgname}" -trimpath -ldflags='-s -w' {{.GoImportPath}}
}

package() {
  install -Dm755 "${pkgname}" "${pkgdir}/usr/bin/${pkgname}"
}
`

// TemplateData holds the values for PKGBUILD generation.
type TemplateData struct {
	PkgName      string
	PkgVer       string
	PkgDesc      string
	URL          string
	Package      string
	GoImportPath string
	EnvVars      []string
}

// Generate writes a PKGBUILD to the given writer for the specified binary.
func Generate(w io.Writer, b *db.Binary) error {
	version := b.Version
	if version == "" || version == "latest" {
		return fmt.Errorf("cannot generate PKGBUILD for %q: no version tag available (version is %q)", b.Name, version)
	}
	// Strip leading 'v' from version for PKGBUILD convention
	pkgVer := strings.TrimPrefix(version, "v")

	desc := b.Description
	// Escape single quotes in description
	desc = strings.ReplaceAll(desc, "'", "'\"'\"'")
	if desc == "" {
		desc = fmt.Sprintf("Go binary: %s", b.Name)
	}

	url := b.RepoURL
	if url == "" {
		url = "https://" + b.Package
	}

	var envVars []string
	flags := b.EnvFlags()
	if flags != "" {
		for _, f := range strings.Split(flags, " ") {
			envVars = append(envVars, f)
		}
	}

	data := TemplateData{
		PkgName:      b.Name,
		PkgVer:       pkgVer,
		PkgDesc:      desc,
		URL:          url,
		Package:      b.Package,
		GoImportPath: b.Package,
		EnvVars:      envVars,
	}

	tmpl, err := template.New("PKGBUILD").Parse(pkgbuildTemplate)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}
	return tmpl.Execute(w, data)
}


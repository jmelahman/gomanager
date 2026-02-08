package cmd

import (
	"database/sql"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/jmelahman/gomanager/internal/state"
	"github.com/spf13/cobra"
)

// dangerousNames are binary names that could shadow critical system tools
// if placed on PATH (e.g. in $HOME/go/bin). Installing a package with one
// of these names could enable a chained attack where a later go install
// invokes the malicious binary instead of the real tool.
var dangerousNames = map[string]bool{
	"cc": true, "gcc": true, "clang": true, "c++": true, "g++": true,
	"ld": true, "as": true, "ar": true, "nm": true,
	"sh": true, "bash": true, "zsh": true, "dash": true,
	"git": true, "ssh": true, "scp": true, "curl": true, "wget": true,
	"make": true, "cmake": true, "pkg-config": true,
	"python": true, "python3": true, "perl": true, "ruby": true,
	"go": true, "gofmt": true,
	"env": true, "sudo": true, "su": true, "xargs": true,
}

func init() {
	rootCmd.AddCommand(installCmd)
}

// resolveBinary looks up a binary by name or package path. If the argument
// looks like a Go module path (contains a slash), it resolves by package path.
// If multiple packages share the same name, the user is prompted to pick one.
func resolveBinary(conn *sql.DB, arg string) (*db.Binary, error) {
	// If it looks like a package path, look up directly
	if strings.Contains(arg, "/") {
		return db.GetByPackage(conn, arg)
	}

	matches, err := db.FindByName(conn, arg)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("binary %q not found in database", arg)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// Multiple matches — ask the user to pick
	fmt.Printf("Multiple packages named %q:\n", arg)
	for i, m := range matches {
		status := m.BuildStatus
		if status == "" {
			status = "unknown"
		}
		fmt.Printf("  [%d] %s (%s, %d stars)\n", i+1, m.Package, status, m.Stars)
	}
	fmt.Printf("Select [1-%d]: ", len(matches))

	var choice int
	if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(matches) {
		return nil, fmt.Errorf("invalid selection")
	}
	return &matches[choice-1], nil
}

var installCmd = &cobra.Command{
	Use:   "install <name or package>",
	Short: "Install a Go binary by name or package path",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDB(); err != nil {
			return err
		}
		conn, err := db.Open()
		if err != nil {
			return err
		}
		defer conn.Close()

		b, err := resolveBinary(conn, args[0])
		if err != nil {
			return err
		}

		if dangerousNames[b.Name] {
			fmt.Printf("Warning: %q shadows a common system tool.\n", b.Name)
			fmt.Printf("  If $HOME/go/bin is on your PATH, this could intercept calls\n")
			fmt.Printf("  to the real %q by other tools (including go install).\n", b.Name)
			fmt.Print("Continue anyway? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(answer) != "y" {
				return nil
			}
		}

		if b.BuildStatus == "failed" {
			fmt.Printf("Warning: %q is marked as a failed build.\n", b.Name)
			fmt.Printf("  Error: %s\n", b.BuildError)
			fmt.Print("Continue anyway? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(answer) != "y" {
				return nil
			}
		}

		installCmd := b.InstallCommand()
		fmt.Printf("Running: %s\n", installCmd)

		return runGoInstall(b)
	},
}

func runGoInstall(b *db.Binary) error {
	version := b.Version
	if version == "" {
		version = "latest"
	}
	pkg := fmt.Sprintf("%s@%s", b.Package, version)

	goCmd := osexec.Command("go", "install", pkg)
	goCmd.Stdout = os.Stdout
	goCmd.Stderr = os.Stderr

	// Apply build flags as environment variables
	goCmd.Env = os.Environ()
	flags := b.EnvFlags()
	if flags != "" {
		for _, f := range strings.Split(flags, " ") {
			goCmd.Env = append(goCmd.Env, f)
		}
	}

	if err := goCmd.Run(); err != nil {
		return fmt.Errorf("go install failed: %w", err)
	}

	// Track installation
	st, err := state.Load()
	if err != nil {
		fmt.Printf("Warning: could not save install state: %v\n", err)
		return nil
	}
	st.MarkInstalled(b.Name, b.Package, version)
	if err := st.Save(); err != nil {
		fmt.Printf("Warning: could not save install state: %v\n", err)
	}

	fmt.Printf("Successfully installed %s\n", b.Name)
	return nil
}

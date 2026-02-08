package cmd

import (
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

var installCmd = &cobra.Command{
	Use:   "install <name>",
	Short: "Install a Go binary by name",
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

		b, err := db.GetByName(conn, args[0])
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

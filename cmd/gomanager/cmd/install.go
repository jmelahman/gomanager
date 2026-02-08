package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/db"
	"github.com/jmelahman/gomanager/cmd/gomanager/internal/state"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(installCmd)
}

var installCmd = &cobra.Command{
	Use:   "install <name>",
	Short: "Install a Go binary by name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := db.Open()
		if err != nil {
			return err
		}
		defer conn.Close()

		b, err := db.GetByName(conn, args[0])
		if err != nil {
			return err
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

	goCmd := exec.Command("go", "install", pkg)
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

package cmd

import (
	"fmt"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/jmelahman/gomanager/internal/state"
	"github.com/spf13/cobra"
)

var upgradeAll bool

func init() {
	upgradeCmd.Flags().BoolVar(&upgradeAll, "all", false, "Upgrade all installed binaries")
	rootCmd.AddCommand(upgradeCmd)
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [name]",
	Short: "Upgrade installed Go binaries to their latest database version",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !upgradeAll && len(args) == 0 {
			return fmt.Errorf("specify a binary name or use --all")
		}

		if err := ensureDB(); err != nil {
			return err
		}
		conn, err := db.Open()
		if err != nil {
			return err
		}
		defer conn.Close()

		st, err := state.Load()
		if err != nil {
			return err
		}

		var toUpgrade []string
		if upgradeAll {
			for name := range st.Installed {
				toUpgrade = append(toUpgrade, name)
			}
		} else {
			toUpgrade = args
		}

		if len(toUpgrade) == 0 {
			fmt.Println("No binaries to upgrade.")
			return nil
		}

		for _, name := range toUpgrade {
			b, err := db.GetByName(conn, name)
			if err != nil {
				fmt.Printf("Skipping %s: %v\n", name, err)
				continue
			}

			installed, ok := st.Installed[name]
			if ok && installed.Version == b.Version {
				fmt.Printf("%s is already at %s\n", name, b.Version)
				continue
			}

			fmt.Printf("Upgrading %s: %s -> %s\n", name, installed.Version, b.Version)
			if err := runGoInstall(b); err != nil {
				fmt.Printf("Failed to upgrade %s: %v\n", name, err)
			}
		}

		return nil
	},
}

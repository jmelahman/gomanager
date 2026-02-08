package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/jmelahman/gomanager/cmd/gomanager/internal/state"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed Go binaries",
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := state.Load()
		if err != nil {
			return err
		}

		if len(st.Installed) == 0 {
			fmt.Println("No binaries installed via gomanager.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "NAME\tPACKAGE\tVERSION\tINSTALLED\n")
		for _, b := range st.Installed {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				b.Name, b.Package, b.Version,
				b.InstalledAt.Format("2006-01-02"))
		}
		w.Flush()
		return nil
	},
}

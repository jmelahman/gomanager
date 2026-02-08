package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/jmelahman/gomanager/internal/db"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(searchCmd)
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for Go binaries in the database",
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

		results, err := db.Search(conn, args[0])
		if err != nil {
			return fmt.Errorf("search failed: %w", err)
		}

		if len(results) == 0 {
			fmt.Println("No results found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "NAME\tSTARS\tSTATUS\tVERSION\tDESCRIPTION\n")
		for _, b := range results {
			desc := b.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n",
				b.Name, b.Stars, b.BuildStatus, b.Version, desc)
		}
		w.Flush()
		return nil
	},
}

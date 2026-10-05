package cli

import (
	"fmt"
	"strings"

	"github.com/cloudticon/ct/internal/scaffold"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var dir string
	var force bool

	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Initialize a new ct project",
		Long:  "Creates main.ct, values.json and AGENTS.md (instructions for AI coding agents) in the target directory. An existing main.ct or values.json is kept unless --force is given; an existing AGENTS.md is always kept.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if cmd.Flags().Changed("dir") && args[0] != dir {
					return fmt.Errorf("give the directory either as an argument or with --dir, not both")
				}
				dir = args[0]
			}
			written, err := scaffold.Init(dir, scaffold.Options{Force: force})
			if err != nil {
				return fmt.Errorf("init failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized ct project in %s/ (%s)\n", strings.TrimSuffix(dir, "/"), strings.Join(written, ", "))
			fmt.Fprintf(cmd.OutOrStdout(), "Next: ct template my-app %s -n default\n", dir)
			return nil
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", ".", "project directory")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

func init() {
	rootCmd.AddCommand(newInitCmd())
}

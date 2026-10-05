package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/spf13/cobra"
)

var version = "dev"

var errorFormat string

var rootCmd = &cobra.Command{
	Use:           "ct",
	Short:         "ct - Kubernetes manifest generator",
	Long:          "ct generates Kubernetes manifests from .ct definitions using a registration model.",
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if errorFormat != "text" && errorFormat != "json" {
			return fmt.Errorf("unsupported --error-format %q (expected text or json)", errorFormat)
		}
		return nil
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.PersistentFlags().StringVar(&errorFormat, "error-format", "text", "how to print errors: text, or json for tools and AI agents")
}

func Execute() error {
	return rootCmd.Execute()
}

// PrintError writes err to w in the format chosen with --error-format.
// Diagnostics keep their structure (code, file, line, resource, path, hint)
// in JSON so tools can act on them without parsing text.
func PrintError(w io.Writer, err error) {
	var list diag.List
	if !errors.As(err, &list) {
		list = diag.List{{Code: diag.CodeError, Message: err.Error()}}
	}
	if errorFormat == "json" {
		_, _ = w.Write(diag.JSON(list))
		return
	}
	if direct, ok := err.(diag.List); ok && len(direct) > 1 {
		fmt.Fprintf(w, "Error: %d problems\n%s\n", len(direct), direct)
		return
	}
	fmt.Fprintf(w, "Error: %s\n", err)
}

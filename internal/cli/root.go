package cli

import (
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
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
		switch errorFormat {
		case "text":
		case "json":
			// Keep stderr a single JSON document: drop progress lines
			// ("applied ...", "deleted ...") that would interleave with it.
			log.SetOutput(io.Discard)
			cmd.Root().SetErr(io.Discard)
		default:
			return fmt.Errorf("unsupported --error-format %q (expected text or json)", errorFormat)
		}
		quietClientGo()
		return nil
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.PersistentFlags().StringVar(&errorFormat, "error-format", "text", "how to print errors: text, or json for tools and AI agents")
}

// quietClientGo mutes client-go's klog output (transient trouble such as a
// port-forward to a pod that just went away, which ct reports itself) and
// prints API server warnings (deprecated APIs, unknown fields) once each,
// through the same writer as ct's own progress lines.
func quietClientGo() {
	klog.SetLogger(logr.Discard())
	rest.SetDefaultWarningHandler(rest.NewWarningWriter(log.Writer(), rest.WarningWriterOptions{Deduplicate: true}))
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

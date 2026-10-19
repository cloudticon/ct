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
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageError(cmd, fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()))
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch errorFormat {
		case "text":
		case "json":
			// Keep stderr a single JSON document: drop progress lines
			// ("applied ...", "deleted ...") that would interleave with it.
			log.SetOutput(io.Discard)
			cmd.Root().SetErr(io.Discard)
		default:
			return usageError(cmd, fmt.Errorf("unsupported --error-format %q (expected text or json)", errorFormat))
		}
		quietClientGo()
		return nil
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return usageError(cmd, err) })
	rootCmd.Long += "\n\nExit codes:\n  0  success\n  1  error in .ct code, values or manifests (render and validation)\n  2  wrong use of ct (arguments, flags, unknown command)\n  3  cluster error (connection, authorization, rejected request)"
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
	if errors.Is(err, errReported) {
		return
	}
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

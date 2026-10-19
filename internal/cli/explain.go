package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/explain"
	"github.com/spf13/cobra"
)

type explainOpts struct {
	apiVersion string
	outputFmt  string
}

func newExplainCmd() *cobra.Command {
	var opts explainOpts
	cmd := &cobra.Command{
		Use:   "explain <Kind>[.<field>...]",
		Short: "Show the fields of a Kubernetes kind offline",
		Long: "Shows the fields of a built-in Kubernetes kind, or of one field path inside it, with types, " +
			"whether they are required and their descriptions. It needs no cluster and no network: the schema " +
			"is the one ct validates against, from the Kubernetes version ct was built with. Custom resources (CRDs) are not covered.",
		Example: "ct explain Deployment.spec.strategy\nct explain Pod.spec.containers -o json",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExplain(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.apiVersion, "api-version", "", "apiVersion of the kind when it is served under several (e.g. autoscaling/v1)")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "", "output format: text or json")
	return cmd
}

func init() {
	rootCmd.AddCommand(newExplainCmd())
}

func runExplain(cmd *cobra.Command, query string, opts explainOpts) error {
	format := strings.ToLower(opts.outputFmt)
	if format != "" && format != "text" && format != "json" {
		return usageError(cmd, fmt.Errorf("unsupported output format: %s", opts.outputFmt))
	}
	res, err := explain.Explain(query, opts.apiVersion)
	if err != nil {
		return explainError(cmd, err)
	}
	if format == "json" {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	return writeExplainText(cmd.OutOrStdout(), res)
}

// explainError reports an unknown kind or field as wrong use of ct, with the
// closest known name so the next call can be right.
func explainError(cmd *cobra.Command, err error) error {
	var nf *explain.NotFoundError
	if !errors.As(err, &nf) {
		return usageError(cmd, err)
	}
	d := diag.Diagnostic{Message: nf.Error()}
	if nf.What == "kind" {
		d.Code = diag.CodeUnknownKind
		if nf.Closest != "" {
			d.Hint = fmt.Sprintf("did you mean %q? kinds are case-insensitive, e.g. `ct explain %s`", nf.Closest, nf.Closest)
		}
	} else {
		d.Code = diag.CodeUnknownField
		d.Path = nf.Path
		if nf.Closest != "" {
			d.Hint = fmt.Sprintf("did you mean %q (`ct explain %s.%s`)? fields of %s: %s",
				nf.Closest, nf.Parent, nf.Closest, nf.Parent, strings.Join(nf.Known, ", "))
		} else {
			d.Hint = fmt.Sprintf("%s has no fields to explain", nf.Parent)
		}
	}
	return &exitError{code: ExitCodeUsage, err: diag.List{d}}
}

func writeExplainText(w io.Writer, res *explain.Result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "KIND:       %s\nVERSION:    %s\n\n", res.Kind, res.APIVersion)
	if res.Path != "" {
		req := ""
		if res.Required {
			req = " -required-"
		}
		fmt.Fprintf(&b, "FIELD: %s <%s>%s\n\n", res.Path, res.Type, req)
	}
	if res.Description != "" {
		b.WriteString("DESCRIPTION:\n")
		b.WriteString(indent(res.Description, "    "))
		b.WriteString("\n")
	}
	if len(res.Fields) > 0 {
		b.WriteString("FIELDS:\n")
		for _, f := range res.Fields {
			req := ""
			if f.Required {
				req = " -required-"
			}
			fmt.Fprintf(&b, "  %s\t<%s>%s\n", f.Name, f.Type, req)
			if f.Description != "" {
				b.WriteString(indent(f.Description, "    "))
			}
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func indent(text, prefix string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		b.WriteString(strings.TrimRight(prefix+line, " "))
		b.WriteString("\n")
	}
	return b.String()
}

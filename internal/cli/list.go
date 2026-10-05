package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type listOpts struct {
	namespace     string
	allNamespaces bool
	context       string
	outputFmt     string
}

func newListCmd() *cobra.Command {
	var opts listOpts

	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List releases tracked by ct inventory",
		Long:  "Lists release inventory ConfigMaps managed by ct and shows release name, namespace, and tracked resources count.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "namespace to search")
	cmd.Flags().BoolVarP(&opts.allNamespaces, "all-namespaces", "A", false, "list releases across all namespaces")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context to use")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "", "output format: table, json or yaml")

	return cmd
}

func init() {
	rootCmd.AddCommand(newListCmd())
}

func runList(cmd *cobra.Command, opts listOpts) error {
	cluster, err := newClusterFn(opts.context, opts.namespace)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	releases, err := cluster.ListReleases(cmd.Context(), opts.namespace, opts.allNamespaces)
	if err != nil {
		return fmt.Errorf("listing releases: %w", err)
	}

	switch strings.ToLower(opts.outputFmt) {
	case "", "table":
		return writeReleaseTable(cmd.OutOrStdout(), releases)
	case "json":
		return writeReleaseJSON(cmd.OutOrStdout(), releases)
	case "yaml":
		return writeReleaseYAML(cmd.OutOrStdout(), releases)
	default:
		return fmt.Errorf("unsupported output format: %s", opts.outputFmt)
	}
}

func writeReleaseTable(w io.Writer, releases []k8s.ReleaseInfo) error {
	data := [][]string{{"NAME", "NAMESPACE", "RESOURCES"}}
	for _, r := range releases {
		data = append(data, []string{r.Name, r.Namespace, fmt.Sprintf("%d", r.Resources)})
	}
	table, err := pterm.DefaultTable.
		WithHasHeader().
		WithData(pterm.TableData(data)).
		WithWriter(w).
		Srender()
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(w, table)
	return err
}

func writeReleaseJSON(w io.Writer, releases []k8s.ReleaseInfo) error {
	data, err := json.MarshalIndent(releases, "", "  ")
	if err != nil {
		return fmt.Errorf("json marshal error: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(data)); err != nil {
		return err
	}
	return nil
}

func writeReleaseYAML(w io.Writer, releases []k8s.ReleaseInfo) error {
	data, err := yaml.Marshal(releases)
	if err != nil {
		return fmt.Errorf("yaml marshal error: %w", err)
	}
	if _, err := fmt.Fprint(w, string(data)); err != nil {
		return err
	}
	return nil
}

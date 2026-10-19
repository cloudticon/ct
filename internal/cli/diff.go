package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	var opts applyOpts

	cmd := &cobra.Command{
		Use:   "diff <name> <dir|repo>",
		Short: "Show what ct apply would create, change and prune",
		Long: "Renders the release like ct apply, sends every object to the API server as a dry run and compares the result " +
			"with the live object. Prints each object's action (create, change, none) with a diff, the objects prune would " +
			"delete, and a one-line summary. Nothing is written to the cluster, the inventory included. Secret values are masked.",
		Example: "ct diff my-app . -n production\nct diff my-app . -n production -o json",
		Args:    usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, args[0], args[1], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "target namespace for resources")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "", "output format: text or json (default text)")
	addRenderFlags(cmd, &opts.templateOpts)
	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false, "re-download the remote source and imported packages instead of using ~/.ct/cache")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context to use")
	cmd.Flags().BoolVar(&opts.createNamespace, "create-namespace", false, "plan as if apply created the namespace first")

	return cmd
}

func init() {
	rootCmd.AddCommand(newDiffCmd())
}

func runDiff(cmd *cobra.Command, releaseName, source string, opts applyOpts) error {
	format := strings.ToLower(opts.outputFmt)
	if format != "" && format != "text" && format != "json" {
		return usageError(cmd, fmt.Errorf("unsupported output format: %s (use text or json)", opts.outputFmt))
	}

	resources, where, err := renderForApply(releaseName, source, &opts)
	if err != nil {
		return err
	}

	cluster, err := newClusterFn(opts.context, opts.namespace)
	if err != nil {
		return clusterError(fmt.Errorf("creating k8s client: %w", err))
	}

	plan, err := planRelease(cmd.Context(), cluster, releaseName, resources, where, opts)
	if err != nil {
		return err
	}

	if format == "json" {
		objects := plan.Objects
		if objects == nil {
			objects = []k8s.PlannedObject{}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(objects)
	}
	writePlan(cmd.OutOrStdout(), plan, true)
	return nil
}

// writePlan prints one line per object and the summary; withDiff adds the
// diff of every created and changed object.
func writePlan(w io.Writer, plan *k8s.Plan, withDiff bool) {
	for _, o := range plan.Objects {
		fmt.Fprintf(w, "%s %s\n", actionLabel(o.Action), plannedRef(o))
		if withDiff && o.Diff != "" {
			fmt.Fprint(w, colorDiff(o.Diff))
		}
	}
	fmt.Fprintln(w, planSummary(plan))
}

func actionLabel(a k8s.Action) string {
	label := fmt.Sprintf("%-6s", a)
	switch a {
	case k8s.ActionCreate:
		return color.GreenString(label)
	case k8s.ActionChange:
		return color.YellowString(label)
	case k8s.ActionPrune:
		return color.HiRedString(label)
	}
	return label
}

func plannedRef(o k8s.PlannedObject) string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s %s %s", o.APIVersion, o.Kind, o.Name)
	}
	return fmt.Sprintf("%s %s %s/%s", o.APIVersion, o.Kind, o.Namespace, o.Name)
}

func colorDiff(diff string) string {
	if color.NoColor {
		return diff
	}
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "@@"):
			lines[i] = color.CyanString(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = color.GreenString(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = color.RedString(l)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// planSummary is the one-line count of a plan.
func planSummary(plan *k8s.Plan) string {
	return fmt.Sprintf("Plan: %d to create, %d to change, %d to prune, %d unchanged.",
		plan.Count(k8s.ActionCreate), plan.Count(k8s.ActionChange), plan.Count(k8s.ActionPrune), plan.Count(k8s.ActionNone))
}

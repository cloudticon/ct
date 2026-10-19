package cli

import (
	"fmt"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

type deleteOpts struct {
	namespace string
	context   string
	dryRun    bool
}

func newDeleteCmd() *cobra.Command {
	var opts deleteOpts

	cmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a release from cluster using inventory",
		Long:    "Loads release inventory from Kubernetes ConfigMap, deletes tracked resources, then removes the inventory ConfigMap.",
		Example: "ct delete my-app -n production\nct delete my-app -n production --dry-run",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "namespace that contains release inventory")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context to use (required when stdin is not a terminal)")

	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "list the resources that would be deleted without changing the cluster")

	return cmd
}

func init() {
	rootCmd.AddCommand(newDeleteCmd())
}

func runDelete(cmd *cobra.Command, releaseName string, opts deleteOpts) error {
	if err := requireKubeContext(cmd, opts.context); err != nil {
		return err
	}
	cluster, err := newClusterFn(opts.context, opts.namespace)
	if err != nil {
		return clusterError(fmt.Errorf("creating k8s client: %w", err))
	}

	if opts.dryRun {
		return planDelete(cmd, cluster, releaseName, opts)
	}

	deleted, err := cluster.DeleteRelease(cmd.Context(), opts.namespace, releaseName)
	if err != nil {
		return clusterError(err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "%s release %s (%d resources)\n", color.HiRedString("deleted"), releaseName, deleted)
	return nil
}

// planDelete prints what delete would remove, in deletion order, and changes nothing.
func planDelete(cmd *cobra.Command, cluster k8s.Cluster, releaseName string, opts deleteOpts) error {
	refs, namespace, err := cluster.PlanDelete(cmd.Context(), opts.namespace, releaseName)
	if err != nil {
		return clusterError(err)
	}
	if len(refs) == 0 {
		return diag.Errorf(diag.CodeError, "release %q not found in namespace %q; nothing to delete", releaseName, namespace)
	}

	out := cmd.OutOrStdout()
	for _, ref := range refs {
		line := ref.Kind + "/" + ref.Name
		if ref.Namespace != "" {
			line += " (namespace " + ref.Namespace + ")"
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "dry run: release %s would delete %d resources; nothing changed\n", releaseName, len(refs))
	return nil
}

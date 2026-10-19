package cli

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

type deleteOpts struct {
	namespace string
	context   string
}

func newDeleteCmd() *cobra.Command {
	var opts deleteOpts

	cmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a release from cluster using inventory",
		Long:    "Loads release inventory from Kubernetes ConfigMap, deletes tracked resources, then removes the inventory ConfigMap.",
		Example: "ct delete my-app -n production",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "namespace that contains release inventory")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context to use")

	return cmd
}

func init() {
	rootCmd.AddCommand(newDeleteCmd())
}

func runDelete(cmd *cobra.Command, releaseName string, opts deleteOpts) error {
	cluster, err := newClusterFn(opts.context, opts.namespace)
	if err != nil {
		return clusterError(fmt.Errorf("creating k8s client: %w", err))
	}

	deleted, err := cluster.DeleteRelease(cmd.Context(), opts.namespace, releaseName)
	if err != nil {
		return clusterError(err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "%s release %s (%d resources)\n", color.HiRedString("deleted"), releaseName, deleted)
	return nil
}

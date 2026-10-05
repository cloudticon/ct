package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudticon/ct/internal/output"
	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type applyOpts struct {
	templateOpts
	context         string
	createNamespace bool
}

var resolveSourceDirForApply = resolveSourceDir
var renderResourcesForApply = renderResources

func newApplyCmd() *cobra.Command {
	var opts applyOpts

	cmd := &cobra.Command{
		Use:   "apply <name> <dir|repo>",
		Short: "Render and apply Kubernetes manifests to a cluster",
		Long:  "Bundles and executes main.ct from the given directory, then applies the resulting manifests via server-side apply. Injects release labels, tracks inventory, and prunes orphaned resources.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(cmd, args[0], args[1], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "target namespace for resources")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "", "output format: yaml or json (default: no output)")
	addRenderFlags(cmd, &opts.templateOpts)
	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false, "re-download the remote source and imported packages instead of using ~/.ct/cache")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context to use")
	cmd.Flags().BoolVar(&opts.createNamespace, "create-namespace", false, "create namespace if it does not exist")

	return cmd
}

func init() {
	rootCmd.AddCommand(newApplyCmd())
}

func runApply(cmd *cobra.Command, releaseName, source string, opts applyOpts) error {
	if err := validateReleaseName(releaseName); err != nil {
		return err
	}
	resolvedDir, err := resolveSourceDirForApply(source, opts.noCache)
	if err != nil {
		return err
	}

	opts.templateOpts.releaseName = releaseName
	resources, err := renderResourcesForApply(resolvedDir, opts.templateOpts)
	if err != nil {
		return err
	}

	resources = k8s.InjectReleaseLabels(resources, releaseName)

	cluster, err := newClusterFn(opts.context, opts.namespace)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	if err := ensureApplyNamespace(cmd.Context(), cluster, opts.namespace, opts.createNamespace); err != nil {
		return err
	}

	if err := cluster.ApplyRelease(cmd.Context(), opts.namespace, releaseName, resources); err != nil {
		return applyError(err)
	}

	if opts.outputFmt != "" {
		out, err := output.Serialize(toOutputResources(resources), opts.outputFmt)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
	}

	return nil
}

// applyError turns common cluster errors into diagnostics with a next step.
func applyError(err error) error {
	var status *apierrors.StatusError
	if errors.As(err, &status) && apierrors.IsNotFound(err) && status.ErrStatus.Details != nil &&
		status.ErrStatus.Details.Kind == "namespaces" {
		return diag.List{{
			Code:    "namespace-not-found",
			Message: fmt.Sprintf("apply failed: %v", err),
			Hint:    "pass --create-namespace, or register a Namespace object in main.ct",
		}}
	}
	return fmt.Errorf("apply failed: %w", err)
}

func ensureApplyNamespace(ctx context.Context, cluster k8s.Cluster, namespace string, createNamespace bool) error {
	if !createNamespace || namespace == "" {
		return nil
	}
	if err := cluster.EnsureNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("ensuring namespace %q: %w", namespace, err)
	}
	return nil
}

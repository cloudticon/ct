package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudticon/ct/internal/output"
	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type applyOpts struct {
	templateOpts
	context         string
	createNamespace bool
}

var resolveSourceDirForApply = resolveSourceDir
var renderResourcesForApply = renderWithOrigins

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
	opts.templateOpts.sourceURL = remoteSourceURL(source)
	resources, where, err := renderResourcesForApply(resolvedDir, opts.templateOpts)
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
		return applyError(err, where)
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

// applyError turns cluster errors into diagnostics with a next step. An
// error tied to one object names it and points at the code that registered
// it; field errors from the API server keep their field path.
func applyError(err error, where origins) error {
	var objErr *k8s.ObjectError
	if !errors.As(err, &objErr) {
		if d, ok := namespaceNotFound(err); ok {
			return diag.List{d}
		}
		return fmt.Errorf("apply failed: %w", err)
	}

	base := diag.Diagnostic{Resource: manifest.Ref(objErr.Object)}.AtChain(where.of(objErr.Object))
	var unknown *k8s.UnknownKindError
	if errors.As(err, &unknown) {
		d := base
		d.Code = diag.CodeUnknownKind
		d.Message = unknown.Error()
		d.Hint = "install the CRD that defines this kind (or register it in main.ct; ct applies CRDs first), " +
			"or fix a typo in apiVersion/kind; `kubectl api-resources` lists what the cluster serves"
		return diag.List{d}
	}
	if d, ok := namespaceNotFound(err); ok {
		d.Resource, d.File, d.Line, d.Column, d.Stack = base.Resource, base.File, base.Line, base.Column, base.Stack
		return diag.List{d}
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		d := base
		d.Code = diag.CodeForbidden
		d.Message = fmt.Sprintf("the cluster refused access: %s", statusMessage(objErr.Err))
		d.Hint = "check that --context points at the cluster you mean and that its user may create and patch this kind " +
			"in this namespace (`kubectl auth can-i patch <resource> -n <namespace> --context <context>`)"
		return diag.List{d}
	}
	if list := fieldDiagnostics(objErr.Err, base); len(list) > 0 {
		return list
	}
	d := base
	d.Code = diag.CodeError
	d.Message = fmt.Sprintf("apply failed: %s", statusMessage(objErr.Err))
	return diag.List{d}
}

func namespaceNotFound(err error) (diag.Diagnostic, bool) {
	var status *apierrors.StatusError
	if errors.As(err, &status) && apierrors.IsNotFound(err) && status.ErrStatus.Details != nil &&
		status.ErrStatus.Details.Kind == "namespaces" {
		return diag.Diagnostic{
			Code:    diag.CodeNamespaceNotFound,
			Message: fmt.Sprintf("apply failed: %v", status),
			Hint:    "pass --create-namespace, or register a Namespace object in main.ct",
		}, true
	}
	return diag.Diagnostic{}, false
}

func statusMessage(err error) string {
	var status *apierrors.StatusError
	if errors.As(err, &status) && status.ErrStatus.Message != "" {
		return status.ErrStatus.Message
	}
	return err.Error()
}

// undeclaredField matches the server-side apply error for a field the
// object's schema doesn't have: "... .spec.replcas: field not declared in schema".
var undeclaredField = regexp.MustCompile(`\.([A-Za-z0-9_.\[\]-]+): field not declared in schema`)

// fieldDiagnostics turns an "invalid" or "bad request" answer from the API
// server into one diagnostic per rejected field.
func fieldDiagnostics(err error, base diag.Diagnostic) diag.List {
	var status *apierrors.StatusError
	if !errors.As(err, &status) || !(apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)) {
		return nil
	}
	var list diag.List
	if details := status.ErrStatus.Details; details != nil {
		for _, cause := range details.Causes {
			if cause.Field == "" {
				continue
			}
			d := base
			d.Path = strings.TrimPrefix(cause.Field, ".")
			d.Message = cause.Message
			d.Code, d.Hint = fieldCode(cause.Type, cause.Message)
			list = append(list, d)
		}
	}
	if len(list) > 0 {
		return list
	}
	for _, m := range undeclaredField.FindAllStringSubmatch(status.ErrStatus.Message, -1) {
		d := base
		d.Code = diag.CodeUnknownField
		d.Path = m[1]
		d.Message = "field not declared in schema"
		d.Hint = "check the field name and nesting against the schema of this kind (`kubectl explain`)"
		list = append(list, d)
	}
	if len(list) > 0 {
		return list
	}
	d := base
	d.Code = diag.CodeInvalidValue
	d.Message = status.ErrStatus.Message
	return diag.List{d}
}

func fieldCode(t metav1.CauseType, message string) (code, hint string) {
	switch {
	case strings.Contains(message, "field is immutable"):
		return diag.CodeInvalidValue, "this field can't change on an existing object: keep the old value, " +
			"or delete the object (or the release) and apply again to recreate it"
	case strings.Contains(message, "field not declared in schema"):
		return diag.CodeUnknownField, "check the field name and nesting against the schema of this kind (`kubectl explain`)"
	case t == metav1.CauseTypeFieldValueRequired:
		return diag.CodeMissingField, ""
	}
	return diag.CodeInvalidValue, ""
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

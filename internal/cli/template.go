package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudticon/ct/internal/output"
	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/cloudticon/ct/pkg/validate"
	"github.com/spf13/cobra"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

type templateOpts struct {
	namespace       string
	valuesFiles     []string
	outputFmt       string
	setValues       []string
	setStringValues []string
	noCache         bool
	releaseName     string
	validate        bool
	// sourceURL is the package URL of a remote source, "" for a local one.
	sourceURL string
}

func newTemplateCmd() *cobra.Command {
	var opts templateOpts

	cmd := &cobra.Command{
		Use:     "template <name> <dir|repo>",
		Short:   "Render Kubernetes manifests from a ct project",
		Long:    "Bundles and executes main.ct from the given source, injects ct release labels, and prints Kubernetes manifests to stdout.",
		Example: "ct template my-app . -n production",
		Args:    usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplate(cmd, args[0], args[1], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "", "default namespace for resources")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "yaml", "output format: yaml or json")
	addRenderFlags(cmd, &opts)
	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false, "re-download the remote source and imported packages instead of using ~/.ct/cache")

	return cmd
}

func init() {
	rootCmd.AddCommand(newTemplateCmd())
}

func addRenderFlags(cmd *cobra.Command, opts *templateOpts) {
	cmd.Flags().BoolVar(&opts.validate, "validate", true, "check rendered objects against the Kubernetes API schema before output")
	cmd.Flags().StringArrayVarP(&opts.valuesFiles, "values", "f", nil, "values file (JSON or YAML); repeat to deep-merge files left to right; replaces auto-detected values.json/values.yaml")
	cmd.Flags().StringArrayVar(&opts.setValues, "set", nil, "override a value (e.g. --set replicas=5, --set 'annotations.a\\.b/c=x'); numbers, true/false and null are typed")
	cmd.Flags().StringArrayVar(&opts.setStringValues, "set-string", nil, "override a value, always as a string (e.g. --set-string image.tag=1.10)")
}

func runTemplate(cmd *cobra.Command, releaseName, sourceDir string, opts templateOpts) error {
	if err := validateReleaseName(releaseName); err != nil {
		return err
	}
	resolvedDir, err := resolveSourceDir(sourceDir, opts.noCache)
	if err != nil {
		return err
	}

	opts.releaseName = releaseName
	opts.sourceURL = remoteSourceURL(sourceDir)
	resources, err := renderResources(resolvedDir, opts)
	if err != nil {
		return err
	}
	resources = k8s.InjectReleaseLabels(resources, releaseName)

	out, err := output.Serialize(toOutputResources(resources), opts.outputFmt)
	if err != nil {
		return fmt.Errorf("serialization failed: %w", err)
	}

	fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}

func renderResources(dir string, opts templateOpts) ([]engine.Resource, error) {
	resources, _, err := renderWithOrigins(dir, opts)
	return resources, err
}

// origins maps an object's server-side identity to the call chain in the
// user's code that registered it, so errors found later (on apply) can still
// point at main.ct.
type origins map[string][]diag.Frame

func (o origins) of(res engine.Resource) []diag.Frame {
	if o == nil || res == nil {
		return nil
	}
	return o[manifest.Identity(res)]
}

// renderWithOrigins is renderResources that also returns where each object
// was registered.
func renderWithOrigins(dir string, opts templateOpts) ([]engine.Resource, origins, error) {
	entryPoint := filepath.Join(dir, "main.ct")
	if _, err := os.Stat(entryPoint); os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("entry point not found: %s", entryPoint)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving project directory: %w", err)
	}
	tr := engine.NewTranspiler(absDir)
	tr.RefreshPackages = opts.noCache
	if opts.sourceURL != "" {
		tr.MarkFresh(opts.sourceURL) // resolveSourceDir already re-downloaded it
	}

	valuesFiles := resolveValuesFiles(dir, opts.valuesFiles)
	if err := checkValuesFilesExist(dir, valuesFiles); err != nil {
		return nil, nil, err
	}
	values, err := engine.LoadValues(engine.ValuesOpts{
		Files:     valuesFiles,
		Set:       opts.setValues,
		SetString: opts.setStringValues,
	})
	if err != nil {
		return nil, nil, err
	}

	jsCode, err := tr.Bundle(entryPoint)
	if err != nil {
		return nil, nil, err
	}

	result, err := engine.Render(engine.ExecuteOpts{
		JSCode:      jsCode,
		Values:      values,
		Namespace:   opts.namespace,
		ReleaseName: opts.releaseName,
		SourceDir:   absDir,
		Timeout:     renderTimeout,
	})
	var problems diag.List
	if err != nil {
		var dups diag.List
		if result == nil || !errors.As(err, &dups) {
			return nil, nil, err
		}
		problems = append(problems, dups...)
	}
	if opts.validate {
		problems = append(problems, validate.Resources(result.Resources, result.Origin)...)
	}
	if len(problems) > 0 {
		return nil, nil, problems
	}
	resources := result.Resources
	where := make(origins, len(resources))
	for i, res := range resources {
		if o := result.Origin(i); o != nil {
			where[manifest.Identity(res)] = o
		}
	}
	manifest.SortForApply(resources)
	return resources, where, nil
}

// validateReleaseName enforces what the release name ends up in: a label
// value and the inventory ConfigMap name.
func validateReleaseName(name string) error {
	if msgs := utilvalidation.IsDNS1123Label(name); len(msgs) > 0 {
		return diag.List{{
			Code:    diag.CodeInvalidValue,
			Message: fmt.Sprintf("invalid release name %q: %s", name, strings.Join(msgs, "; ")),
			Hint:    "release names are lowercase letters, digits and '-', at most 63 characters, e.g. my-app",
		}}
	}
	return nil
}

// renderTimeout stops runaway .ct programs (endless loops) with a stack trace
// instead of hanging the CLI.
var renderTimeout = time.Minute

// resolveValuesFiles returns the values files for a render, lowest layer
// first. The project's values.json, values.yaml or values.yml (the first one
// found in dir) is always the base layer; explicit -f files are layered on top
// in the given order, like a chart's values.yaml in Helm. Explicit files fall
// back to the project directory, so `-f values-prod.yaml` works for remote
// sources too. A base file that is also passed with -f is not loaded twice.
func resolveValuesFiles(dir string, explicit []string) []string {
	var files []string
	base := detectValuesFile(dir)
	if base != "" {
		files = append(files, base)
	}
	for _, f := range explicit {
		path := f
		if _, err := os.Stat(f); err != nil && !filepath.IsAbs(f) {
			if inProject := filepath.Join(dir, f); fileExists(inProject) {
				path = inProject
			}
		}
		if base != "" && sameFile(base, path) {
			continue
		}
		files = append(files, path)
	}
	return files
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

func detectValuesFile(dir string) string {
	for _, name := range []string{"values.json", "values.yaml", "values.yml"} {
		path := filepath.Join(dir, name)
		if fileExists(path) {
			return path
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func toOutputResources(resources []engine.Resource) []output.Resource {
	result := make([]output.Resource, len(resources))
	for i, r := range resources {
		result[i] = output.Resource(r)
	}
	return result
}

// checkValuesFilesExist reports a values file that does not exist, with the
// values files the project does have as a hint.
func checkValuesFilesExist(dir string, files []string) error {
	var problems diag.List
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			continue
		}
		d := diag.Diagnostic{Code: diag.CodeValues, Message: "values file not found", File: f}
		if found := projectValuesFiles(dir); len(found) > 0 {
			d.Hint = "values files in the project: " + strings.Join(found, ", ")
		} else {
			d.Hint = "the project has no values files (values.json, values.yaml, values-*.yaml)"
		}
		problems = append(problems, d)
	}
	if len(problems) == 0 {
		return nil
	}
	return problems
}

// projectValuesFiles lists values*.json|yaml|yml in dir, sorted by name.
func projectValuesFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "values") {
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".json", ".yaml", ".yml":
			names = append(names, name)
		}
	}
	return names
}

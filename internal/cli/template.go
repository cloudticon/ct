package cli

import (
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
}

func newTemplateCmd() *cobra.Command {
	var opts templateOpts

	cmd := &cobra.Command{
		Use:   "template <name> <dir|repo>",
		Short: "Render Kubernetes manifests from a ct project",
		Long:  "Bundles and executes main.ct from the given source, injects ct release labels, and prints Kubernetes manifests to stdout.",
		Args:  cobra.ExactArgs(2),
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
	entryPoint := filepath.Join(dir, "main.ct")
	if _, err := os.Stat(entryPoint); os.IsNotExist(err) {
		return nil, fmt.Errorf("entry point not found: %s", entryPoint)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving project directory: %w", err)
	}
	tr := engine.NewTranspiler(absDir)
	tr.RefreshPackages = opts.noCache

	values, err := engine.LoadValues(engine.ValuesOpts{
		Files:     resolveValuesFiles(dir, opts.valuesFiles),
		Set:       opts.setValues,
		SetString: opts.setStringValues,
	})
	if err != nil {
		return nil, err
	}

	jsCode, err := tr.Bundle(entryPoint)
	if err != nil {
		return nil, err
	}

	result, err := engine.Render(engine.ExecuteOpts{
		JSCode:      jsCode,
		Values:      values,
		Namespace:   opts.namespace,
		ReleaseName: opts.releaseName,
		SourceDir:   absDir,
		Timeout:     renderTimeout,
	})
	if err != nil {
		return nil, err
	}
	if opts.validate {
		if problems := validate.Resources(result.Resources, result.Origin); len(problems) > 0 {
			return nil, problems
		}
	}
	resources := result.Resources
	manifest.SortForApply(resources)
	return resources, nil
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

// resolveValuesFiles returns the values files for a render. Explicit files
// are used as given, falling back to the project directory, so
// `-f values-prod.yaml` works for remote sources too. Without explicit files
// the first of values.json, values.yaml, values.yml in dir is used.
func resolveValuesFiles(dir string, explicit []string) []string {
	if len(explicit) > 0 {
		files := make([]string, len(explicit))
		for i, f := range explicit {
			files[i] = f
			if _, err := os.Stat(f); err != nil && !filepath.IsAbs(f) {
				if inProject := filepath.Join(dir, f); fileExists(inProject) {
					files[i] = inProject
				}
			}
		}
		return files
	}
	if path := detectValuesFile(dir); path != "" {
		return []string{path}
	}
	return nil
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

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudticon/ct/internal/output"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/spf13/cobra"
)

type templateOpts struct {
	namespace   string
	valuesFile  string
	outputFmt   string
	setValues   []string
	noCache     bool
	releaseName string
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
	cmd.Flags().StringVarP(&opts.valuesFile, "values", "f", "", "path to values file (JSON or YAML, overrides auto-detect)")
	cmd.Flags().StringVarP(&opts.outputFmt, "output", "o", "yaml", "output format: yaml or json")
	cmd.Flags().StringArrayVar(&opts.setValues, "set", nil, "override values (e.g. --set replicas=5)")
	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false, "skip cache, re-download remote source")

	return cmd
}

func init() {
	rootCmd.AddCommand(newTemplateCmd())
}

func runTemplate(cmd *cobra.Command, releaseName, sourceDir string, opts templateOpts) error {
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

	tr := engine.NewTranspiler(dir)

	valuesPath := resolveValuesPath(dir, opts.valuesFile)
	values, err := loadValuesIfPresent(valuesPath, opts.setValues)
	if err != nil {
		return nil, err
	}

	jsCode, err := tr.Bundle(entryPoint)
	if err != nil {
		return nil, fmt.Errorf("bundle failed: %w", err)
	}

	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:      jsCode,
		Values:      values,
		Namespace:   opts.namespace,
		ReleaseName: opts.releaseName,
	})
	if err != nil {
		return nil, err
	}
	manifest.SortForApply(resources)
	return resources, nil
}

func resolveValuesPath(dir, explicit string) string {
	if explicit != "" {
		return explicit
	}
	candidates := []string{"values.json", "values.yaml", "values.yml"}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func loadValuesIfPresent(valuesPath string, setOverrides []string) (map[string]interface{}, error) {
	if valuesPath == "" && len(setOverrides) == 0 {
		return nil, nil
	}
	if valuesPath == "" {
		return nil, fmt.Errorf("--set provided but no values file found")
	}
	values, err := engine.LoadValuesFile(valuesPath, setOverrides)
	if err != nil {
		return nil, fmt.Errorf("loading values from %s: %w", valuesPath, err)
	}
	return values, nil
}

func toOutputResources(resources []engine.Resource) []output.Resource {
	result := make([]output.Resource, len(resources))
	for i, r := range resources {
		result[i] = output.Resource(r)
	}
	return result
}

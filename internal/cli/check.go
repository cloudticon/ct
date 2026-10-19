package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/spf13/cobra"
)

type checkOpts struct {
	templateOpts
}

// checkVariant is one values combination ct check rendered.
type checkVariant struct {
	// Name is the overlay file (values-prod.yaml), the base file when there
	// is no overlay, or the -f files joined with " + ".
	Name string `json:"name"`
	// Files are the values files of the variant, lowest layer first,
	// relative to the project directory when they are inside it.
	Files  []string  `json:"files"`
	OK     bool      `json:"ok"`
	Errors diag.List `json:"errors"`
}

// checkReport is the --error-format json document of ct check.
type checkReport struct {
	OK        bool           `json:"ok"`
	Release   string         `json:"release"`
	Namespace string         `json:"namespace"`
	Variants  []checkVariant `json:"variants"`
}

func newCheckCmd() *cobra.Command {
	var opts checkOpts

	cmd := &cobra.Command{
		Use:   "check [dir]",
		Short: "Render and validate the project with every values file, without printing manifests",
		Long: "Renders and validates main.ct without printing manifests and without a cluster.\n\n" +
			"Without -f it checks the project values file alone and then the base layered with each\n" +
			"values-*.json|yaml|yml of the project, one variant per file. With -f it checks just that\n" +
			"combination. Prints one line per variant; exits 1 when any variant has a problem.",
		Example: "ct check\nct check ./deploy --name shop -n production\nct check . -f values-prod.yaml --set replicas=3",
		Args:    usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCheck(cmd, dir, opts)
		},
	}

	cmd.Flags().StringVar(&opts.releaseName, "name", "", "release name for Release.name (default: the directory name)")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", "default", "namespace for Release.namespace and namespaced objects")
	addRenderFlags(cmd, &opts.templateOpts)

	return cmd
}

func init() {
	rootCmd.AddCommand(newCheckCmd())
}

// errReported marks a failure whose details the command already printed, so
// PrintError adds nothing.
var errReported = errors.New("problems reported")

func runCheck(cmd *cobra.Command, dir string, opts checkOpts) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return usageError(cmd, fmt.Errorf("project directory not found: %s", dir))
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving project directory: %w", err)
	}
	if entry := filepath.Join(dir, "main.ct"); !fileExists(entry) {
		return fmt.Errorf("entry point not found: %s", entry)
	}

	if opts.releaseName == "" {
		opts.releaseName = filepath.Base(absDir)
		if err := validateReleaseName(opts.releaseName); err != nil {
			var list diag.List
			if errors.As(err, &list) {
				list[0].Hint = "the release name defaults to the directory name; pass a valid one with --name, e.g. --name my-app"
			}
			return err
		}
	} else if err := validateReleaseName(opts.releaseName); err != nil {
		return err
	}

	report := checkReport{OK: true, Release: opts.releaseName, Namespace: opts.namespace}
	for _, v := range checkVariants(dir, opts.valuesFiles) {
		vopts := opts.templateOpts
		vopts.valuesFiles = v.files
		variant := checkVariant{Name: v.name, Files: v.display, OK: true, Errors: diag.List{}}
		if _, err := renderResources(dir, vopts); err != nil {
			variant.OK = false
			variant.Errors = asDiagnostics(err)
			report.OK = false
		}
		report.Variants = append(report.Variants, variant)
	}

	if errorFormat == "json" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		_, _ = cmd.OutOrStdout().Write(buf.Bytes())
		if !report.OK {
			return &exitError{code: ExitCodeError, err: errReported}
		}
		return nil
	}

	width := 0
	for _, v := range report.Variants {
		width = max(width, len(v.Name))
	}
	failed := 0
	for _, v := range report.Variants {
		status := "ok"
		if !v.OK {
			failed++
			status = plural(len(v.Errors), "problem")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-*s  %s\n", width, v.Name, status)
	}
	if failed == 0 {
		return nil
	}
	return &checkError{failed: failed, report: report}
}

// checkError lists the diagnostics of the failed variants, each prefixed
// with the variant name.
type checkError struct {
	failed int
	report checkReport
}

func (e *checkError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %s failed", e.failed, plural(len(e.report.Variants), "variant"))
	for _, v := range e.report.Variants {
		for _, d := range v.Errors {
			fmt.Fprintf(&b, "\n%s: %s", v.Name, d)
		}
	}
	return b.String()
}

type valuesVariant struct {
	name    string
	files   []string // passed to the render as -f files
	display []string // all layers, for the report
}

// checkVariants returns the values combinations to check: the -f files as one
// variant, or the base alone followed by the base with each values-* file.
func checkVariants(dir string, explicit []string) []valuesVariant {
	base := detectValuesFile(dir)
	display := func(files []string) []string {
		out := []string{}
		for _, f := range resolveValuesFiles(dir, files) {
			out = append(out, relToDir(dir, f))
		}
		return out
	}
	if len(explicit) > 0 {
		return []valuesVariant{{name: strings.Join(explicit, " + "), files: explicit, display: display(explicit)}}
	}
	baseName := "base"
	if base != "" {
		baseName = filepath.Base(base)
	}
	variants := []valuesVariant{{name: baseName, display: display(nil)}}
	for _, name := range projectValuesFiles(dir) {
		if !strings.HasPrefix(name, "values-") {
			continue
		}
		files := []string{filepath.Join(dir, name)}
		variants = append(variants, valuesVariant{name: name, files: files, display: display(files)})
	}
	return variants
}

func relToDir(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func asDiagnostics(err error) diag.List {
	var list diag.List
	if errors.As(err, &list) {
		return list
	}
	return diag.List{{Code: diag.CodeError, Message: err.Error()}}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

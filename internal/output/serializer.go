package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudticon/ct/pkg/manifest"
	"gopkg.in/yaml.v3"
)

type Resource = map[string]interface{}

func Serialize(resources []Resource, format string) (string, error) {
	for _, r := range resources {
		manifest.Clean(r)
	}

	switch format {
	case "yaml", "":
		return serializeYAML(resources)
	case "json":
		return serializeJSON(resources)
	default:
		return "", fmt.Errorf("unsupported output format: %s", format)
	}
}

func serializeYAML(resources []Resource) (string, error) {
	var docs []string
	for _, r := range resources {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(r); err != nil {
			return "", fmt.Errorf("yaml marshal error: %w", err)
		}
		if err := enc.Close(); err != nil {
			return "", fmt.Errorf("yaml marshal error: %w", err)
		}
		docs = append(docs, strings.TrimRight(buf.String(), "\n"))
	}
	return strings.Join(docs, "\n---\n") + "\n", nil
}

func serializeJSON(resources []Resource) (string, error) {
	if resources == nil {
		resources = []Resource{}
	}
	data, err := json.MarshalIndent(resources, "", "  ")
	if err != nil {
		return "", fmt.Errorf("json marshal error: %w", err)
	}
	return string(data) + "\n", nil
}

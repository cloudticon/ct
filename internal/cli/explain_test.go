package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runExplainCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newExplainCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestExplain_TextWithoutCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/missing")
	out, err := runExplainCmd(t, "Deployment.spec.strategy")
	require.NoError(t, err)
	assert.Contains(t, out, "KIND:       Deployment\nVERSION:    apps/v1\n")
	assert.Contains(t, out, "FIELD: spec.strategy <DeploymentStrategy>")
	assert.Contains(t, out, "  rollingUpdate\t<RollingUpdateDeployment>\n")
	assert.Contains(t, out, "  type\t<string>\n    Type of deployment.")
}

func TestExplain_RequiredMarker(t *testing.T) {
	out, err := runExplainCmd(t, "Deployment.spec")
	require.NoError(t, err)
	assert.Contains(t, out, "  selector\t<LabelSelector> -required-\n")
	assert.Contains(t, out, "  replicas\t<integer>\n")
}

func TestExplain_JSONIsOneDocument(t *testing.T) {
	out, err := runExplainCmd(t, "Deployment.spec.strategy", "-o", "json")
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewBufferString(out))
	var doc map[string]interface{}
	require.NoError(t, dec.Decode(&doc))
	assert.False(t, dec.More(), "a single JSON document")
	assert.Equal(t, "Deployment", doc["kind"])
	assert.Equal(t, "spec.strategy", doc["path"])
	assert.Len(t, doc["fields"], 2)
}

func TestExplain_UnknownFieldSuggestsClosest(t *testing.T) {
	code, out := execExit(t, "explain", "Deployment.spec.replcas")
	assert.Equal(t, ExitCodeUsage, code)
	assert.Contains(t, out, `Deployment.spec has no field "replcas" [unknown-field]`)
	assert.Contains(t, out, `did you mean "replicas"`)
}

func TestExplain_UnknownKindSuggestsClosestInJSON(t *testing.T) {
	code, out := execExit(t, "--error-format", "json", "explain", "Deploymnt")
	assert.Equal(t, ExitCodeUsage, code)
	assert.Contains(t, out, `"code": "unknown-kind"`)
	assert.Contains(t, out, `did you mean \"Deployment\"`)
}

func TestExplain_BadOutputIsUsage(t *testing.T) {
	code, _ := execExit(t, "explain", "Pod", "-o", "yaml")
	assert.Equal(t, ExitCodeUsage, code)
}

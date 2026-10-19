package explain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fieldNames(fs []Field) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func findField(fs []Field, name string) Field {
	for _, f := range fs {
		if f.Name == name {
			return f
		}
	}
	return Field{}
}

func TestExplainKind(t *testing.T) {
	res, err := Explain("Deployment", "")
	require.NoError(t, err)
	assert.Equal(t, "Deployment", res.Kind)
	assert.Equal(t, "apps/v1", res.APIVersion)
	assert.Equal(t, "", res.Path)
	assert.NotEmpty(t, res.Description)
	assert.Equal(t, []string{"apiVersion", "kind", "metadata", "spec", "status"}, fieldNames(res.Fields))
	assert.Equal(t, "DeploymentSpec", findField(res.Fields, "spec").Type)
}

func TestExplainFieldPath(t *testing.T) {
	res, err := Explain("Deployment.spec.strategy", "")
	require.NoError(t, err)
	assert.Equal(t, "spec.strategy", res.Path)
	assert.Equal(t, "DeploymentStrategy", res.Type)
	assert.Contains(t, res.Description, "deployment strategy")
	assert.Equal(t, []string{"rollingUpdate", "type"}, fieldNames(res.Fields))
	assert.Equal(t, "string", findField(res.Fields, "type").Type)
	assert.Contains(t, findField(res.Fields, "type").Description, "Recreate")
}

func TestExplainRequiredAndTypes(t *testing.T) {
	spec, err := Explain("deployment.spec", "")
	require.NoError(t, err)
	assert.True(t, findField(spec.Fields, "selector").Required)
	assert.True(t, findField(spec.Fields, "template").Required)
	assert.False(t, findField(spec.Fields, "replicas").Required)
	assert.Equal(t, "integer", findField(spec.Fields, "replicas").Type)

	c, err := Explain("Pod.spec.containers[0]", "")
	require.NoError(t, err)
	assert.Equal(t, "[]Container", c.Type)
	assert.True(t, findField(c.Fields, "name").Required)
	assert.Equal(t, "[]EnvVar", findField(c.Fields, "env").Type)

	r, err := Explain("Pod.spec.containers.resources", "")
	require.NoError(t, err)
	assert.Equal(t, "map[string]Quantity", findField(r.Fields, "limits").Type)

	p, err := Explain("Service.spec.ports", "")
	require.NoError(t, err)
	assert.Equal(t, "IntOrString", findField(p.Fields, "targetPort").Type)

	m, err := Explain("ConfigMap.metadata", "")
	require.NoError(t, err)
	assert.Equal(t, "map[string]string", findField(m.Fields, "labels").Type)
	assert.NotEmpty(t, findField(m.Fields, "labels").Description)
	assert.Equal(t, "string (date-time)", findField(m.Fields, "creationTimestamp").Type)

	s, err := Explain("Secret", "")
	require.NoError(t, err)
	assert.Equal(t, "map[string]string (base64)", findField(s.Fields, "data").Type)
}

func TestExplainAPIVersion(t *testing.T) {
	res, err := Explain("HorizontalPodAutoscaler", "")
	require.NoError(t, err)
	assert.Equal(t, "autoscaling/v2", res.APIVersion)

	res, err = Explain("HorizontalPodAutoscaler", "autoscaling/v1")
	require.NoError(t, err)
	assert.Equal(t, "autoscaling/v1", res.APIVersion)

	_, err = Explain("Deployment", "v1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apps/v1")

	ev, err := Explain("Event", "")
	require.NoError(t, err)
	assert.Equal(t, "v1", ev.APIVersion, "core group first")
}

func TestExplainUnknown(t *testing.T) {
	_, err := Explain("Deployment.spec.replcas", "")
	var nf *NotFoundError
	require.True(t, errors.As(err, &nf))
	assert.Equal(t, "field", nf.What)
	assert.Equal(t, "Deployment.spec", nf.Parent)
	assert.Equal(t, "spec.replcas", nf.Path)
	assert.Equal(t, "replicas", nf.Closest)
	assert.Contains(t, nf.Known, "strategy")

	_, err = Explain("Deploymnt", "")
	require.True(t, errors.As(err, &nf))
	assert.Equal(t, "kind", nf.What)
	assert.Equal(t, "Deployment", nf.Closest)

	_, err = Explain("ConfigMap.data.foo", "")
	require.True(t, errors.As(err, &nf))
	assert.Equal(t, "ConfigMap.data", nf.Parent)
	assert.Empty(t, nf.Known)
}

func TestFieldPath(t *testing.T) {
	assert.Equal(t, "", FieldPath("replicas"))
	assert.Equal(t, "spec", FieldPath("spec.replcas"))
	assert.Equal(t, "spec.template.spec.containers", FieldPath("spec.template.spec.containers[0].imag"))
}

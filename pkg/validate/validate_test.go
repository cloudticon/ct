package validate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/cloudticon/ct/pkg/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// obj parses a JSON manifest the way the engine hands them over.
func obj(t *testing.T, js string) manifest.Resource {
	t.Helper()
	var res manifest.Resource
	require.NoError(t, json.Unmarshal([]byte(js), &res), js)
	return res
}

func validateOne(t *testing.T, js string) diag.List {
	t.Helper()
	return validate.Resources([]manifest.Resource{obj(t, js)}, nil)
}

const deployment = `{
  "apiVersion": "apps/v1", "kind": "Deployment",
  "metadata": {"name": "web", "namespace": "prod", "labels": {"app": "web"}},
  "spec": {
    "replicas": 2,
    "selector": {"matchLabels": {"app": "web"}},
    "template": {
      "metadata": {"labels": {"app": "web", "tier": "frontend"}},
      "spec": {
        "containers": [{
          "name": "web", "image": "nginx:1.27",
          "ports": [{"name": "http", "containerPort": 8080}],
          "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}],
          "resources": {"requests": {"cpu": "100m", "memory": 128974848}}
        }],
        "volumes": [{"name": "tmp", "emptyDir": {}}]
      }
    }
  }
}`

func TestValidate_AcceptsValidObjects(t *testing.T) {
	valid := []string{
		deployment,
		`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "web"}, "spec": {"selector": {"app": "web"}, "ports": [{"port": 80, "targetPort": "http"}]}}`,
		`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "db"}, "spec": {"clusterIP": "None", "selector": {"app": "db"}}}`,
		`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "ext"}, "spec": {"type": "ExternalName", "externalName": "db.example.com"}}`,
		`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "cfg"}, "data": {"app.properties": "a=b", "LOG_LEVEL": "info"}}`,
		`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "s"}, "stringData": {"password": "plain"}, "data": {"token": "c2VjcmV0"}}`,
		`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "tls"}, "type": "kubernetes.io/tls", "data": {"tls.crt": "Y3J0", "tls.key": "a2V5"}}`,
		`{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "team-a"}}`,
		`{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": {"name": "system:aggregate-to-view"}, "rules": [{"apiGroups": [""], "resources": ["pods"], "verbs": ["get"]}]}`,
		`{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": {"name": "web"}, "spec": {"rules": [{"host": "a.example.com", "http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}}`,
		`{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": {"name": "web"}, "spec": {"scaleTargetRef": {"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}, "minReplicas": 2, "maxReplicas": 5}}`,
		`{"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": {"name": "web"}, "spec": {"minAvailable": 1, "selector": {"matchLabels": {"app": "web"}}}}`,
		`{"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "migrate"}, "spec": {"template": {"spec": {"restartPolicy": "Never", "containers": [{"name": "m", "image": "app:1"}]}}}}`,
		`{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": {"name": "nightly"}, "spec": {"schedule": "0 3 * * *", "jobTemplate": {"spec": {"template": {"spec": {"restartPolicy": "OnFailure", "containers": [{"name": "c", "image": "app:1"}]}}}}}}`,
		`{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "allow-all"}, "spec": {"podSelector": {}, "ingress": [{}]}}`,
		// The API server defaults a volume without a source to emptyDir.
		strings.Replace(deployment, `{"name": "tmp", "emptyDir": {}}`, `{"name": "tmp"}`, 1),
		// StatefulSet containers may mount volumeClaimTemplates.
		`{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "db"}, "spec": {"serviceName": "db", "selector": {"matchLabels": {"app": "db"}}, "volumeClaimTemplates": [{"metadata": {"name": "data"}, "spec": {"accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "1Gi"}}}}], "template": {"metadata": {"labels": {"app": "db"}}, "spec": {"containers": [{"name": "db", "image": "postgres:17", "volumeMounts": [{"name": "data", "mountPath": "/var/lib/postgresql"}]}]}}}}`,
		// targetPort "" defaults to port; clusterIPs [None] is headless.
		`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "web2"}, "spec": {"ports": [{"port": 80, "targetPort": ""}]}}`,
		`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "db2"}, "spec": {"clusterIPs": ["None"], "selector": {"app": "db"}}}`,
		// Native sidecars: init containers with restartPolicy Always.
		strings.Replace(deployment, `"containers": [`, `"initContainers": [{"name": "proxy", "image": "envoy:1", "restartPolicy": "Always"}], "containers": [`, 1),
		`{"apiVersion": "certificates.k8s.io/v1", "kind": "CertificateSigningRequest", "metadata": {"name": "User:Alice"}, "spec": {"request": "Y3Ny", "signerName": "kubernetes.io/kube-apiserver-client", "usages": ["client auth"]}}`,
		// Custom resources: only metadata is checked.
		`{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": {"name": "hello"}, "spec": {"template": {"spec": {"containers": [{"image": "x"}]}}}}`,
		`{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": {"name": "web-tls"}, "spec": {"anything": {"goes": true}}}`,
		`{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": {"name": "widgets.example.com"}, "spec": {"group": "example.com", "names": {"kind": "Widget", "plural": "widgets"}}}`,
	}
	for _, js := range valid {
		res := obj(t, js)
		t.Run(manifest.Ref(res), func(t *testing.T) {
			assert.Empty(t, validate.Resources([]manifest.Resource{res}, nil))
		})
	}
}

type want struct {
	code, path, message string
}

func assertDiagnostics(t *testing.T, got diag.List, wants ...want) {
	t.Helper()
	require.Len(t, got, len(wants), "got: %v", got)
	for i, w := range wants {
		assert.Equal(t, w.code, got[i].Code, "diagnostic %d: %s", i, got[i])
		assert.Equal(t, w.path, got[i].Path, "diagnostic %d: %s", i, got[i])
		assert.Contains(t, got[i].Message, w.message, "diagnostic %d: %s", i, got[i])
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		js    string
		wants []want
	}{
		{
			"ConfigMap data under spec",
			`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "cfg"}, "spec": {"data": {"a": "b"}}}`,
			[]want{{diag.CodeUnknownField, "spec", "unknown field"}},
		},
		{
			"core/v1 is not an apiVersion",
			`{"apiVersion": "core/v1", "kind": "ConfigMap", "metadata": {"name": "cfg"}}`,
			[]want{{diag.CodeUnknownKind, "apiVersion", `unknown apiVersion "core/v1": ConfigMap is served as v1`}},
		},
		{
			"wrong version of a built-in group",
			`{"apiVersion": "apps/v2", "kind": "Deployment", "metadata": {"name": "web"}}`,
			[]want{{diag.CodeUnknownKind, "apiVersion", "Deployment is served as apps/v1"}},
		},
		{
			"removed API version",
			`{"apiVersion": "extensions/v1beta1", "kind": "Ingress", "metadata": {"name": "web"}}`,
			[]want{{diag.CodeInvalidValue, "apiVersion", "removed in Kubernetes 1.22; use networking.k8s.io/v1"}},
		},
		{
			"container fields directly on the Deployment spec",
			`{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "web"}, "spec": {"image": "nginx", "replicas": "3", "selector": {"matchLabels": {"app": "web"}}, "template": {"metadata": {"labels": {"app": "web"}}, "spec": {"containers": [{"name": "web", "image": "nginx"}]}}}}`,
			[]want{
				{diag.CodeInvalidValue, "spec.replicas", "expected an integer, got string"},
				{diag.CodeUnknownField, "spec.image", "unknown field"},
			},
		},
		{
			"typo in a nested field",
			strings.Replace(deployment, `"containerPort": 8080`, `"containerPort": 8080, "protocl": "TCP"`, 1),
			[]want{{diag.CodeUnknownField, "spec.template.spec.containers[0].ports[0].protocl", "unknown field"}},
		},
		{
			"selector doesn't match template labels",
			strings.Replace(deployment, `"matchLabels": {"app": "web"}`, `"matchLabels": {"app": "api"}`, 1),
			[]want{{diag.CodeInvalidValue, "spec.template.metadata.labels", "`selector` does not match template `labels`"}},
		},
		{
			"missing selector, containers",
			`{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "web"}, "spec": {"template": {"spec": {}}}}`,
			[]want{
				{diag.CodeMissingField, "spec.selector", "Required value"},
				{diag.CodeMissingField, "spec.template.spec.containers", "Required value"},
			},
		},
		{
			"container problems",
			strings.NewReplacer(`"image": "nginx:1.27"`, `"image": ""`, `"name": "http"`, `"name": "http-metrics-port"`, `{"name": "tmp", "mountPath": "/tmp"}`, `{"name": "data", "mountPath": "/data"}`).Replace(deployment),
			[]want{
				{diag.CodeMissingField, "spec.template.spec.containers[0].image", "Required value"},
				{diag.CodeInvalidValue, "spec.template.spec.containers[0].ports[0].name", "must be no more than 15 characters"},
				{diag.CodeInvalidValue, "spec.template.spec.containers[0].volumeMounts[0].name", "Not found"},
			},
		},
		{
			"volume with two sources",
			strings.Replace(deployment, `{"name": "tmp", "emptyDir": {}}`, `{"name": "tmp", "emptyDir": {}, "configMap": {"name": "x"}}`, 1),
			[]want{{diag.CodeInvalidValue, "spec.template.spec.volumes[0]", "may not specify more than 1 volume type"}},
		},
		{
			"misspelled built-in kinds",
			`{"apiVersion": "apps/v1", "kind": "Deploymnet", "metadata": {"name": "web"}, "spec": {"junk": 1}}`,
			[]want{{diag.CodeUnknownKind, "kind", `no kind "Deploymnet" in apps/v1; did you mean "Deployment"?`}},
		},
		{
			"wrong case of a core kind",
			`{"apiVersion": "v1", "kind": "Configmap", "metadata": {"name": "cfg"}}`,
			[]want{{diag.CodeUnknownKind, "kind", `did you mean "ConfigMap"?`}},
		},
		{
			"fractional number in an integer field doesn't hide later errors",
			strings.NewReplacer(`"replicas": 2`, `"replicas": 1.5`, `"image": "nginx:1.27",`, `"image": "nginx:1.27", "env": [{"name": "A", "value": 3}],`).Replace(deployment),
			[]want{
				{diag.CodeInvalidValue, "spec.replicas", "expected an integer, got number 1.5"},
				{diag.CodeInvalidValue, "spec.template.spec.containers.env.value", "expected a string, got number"},
			},
		},
		{
			"Deployment with restartPolicy Never",
			strings.Replace(deployment, `"containers": [`, `"restartPolicy": "Never", "containers": [`, 1),
			[]want{{diag.CodeInvalidValue, "spec.template.spec.restartPolicy", `Unsupported value: "Never"`}},
		},
		{
			"Job without restartPolicy",
			`{"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "migrate"}, "spec": {"template": {"spec": {"containers": [{"name": "m", "image": "app:1"}]}}}}`,
			[]want{{diag.CodeInvalidValue, "spec.template.spec.restartPolicy", `supported values: "OnFailure", "Never"`}},
		},
		{
			"CronJob name too long and no schedule",
			`{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": {"name": "` + strings.Repeat("a", 53) + `"}, "spec": {"jobTemplate": {"spec": {"template": {"spec": {"restartPolicy": "Never", "containers": [{"name": "c", "image": "x"}]}}}}}}`,
			[]want{
				{diag.CodeInvalidValue, "metadata.name", "may not be more than 52"},
				{diag.CodeMissingField, "spec.schedule", "Required value"},
			},
		},
		{
			"Service ports",
			`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "web"}, "spec": {"ports": [{"port": 80}, {"port": 70000, "targetPort": "a-very-long-port-name"}]}}`,
			[]want{
				{diag.CodeMissingField, "spec.ports[0].name", "more than one port"},
				{diag.CodeInvalidValue, "spec.ports[1].port", "between 1 and 65535"},
				{diag.CodeMissingField, "spec.ports[1].name", "more than one port"},
				{diag.CodeInvalidValue, "spec.ports[1].targetPort", "no more than 15 characters"},
			},
		},
		{
			"Service without ports",
			`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "web"}, "spec": {"selector": {"app": "web"}}}`,
			[]want{{diag.CodeMissingField, "spec.ports", "Required value"}},
		},
		{
			"Ingress path without pathType",
			`{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": {"name": "web"}, "spec": {"rules": [{"http": {"paths": [{"path": "/", "backend": {"service": {"name": "web", "port": {"number": 80}}}}]}}]}}`,
			[]want{{diag.CodeMissingField, "spec.rules[0].http.paths[0].pathType", "Required value"}},
		},
		{
			"Secret data not base64",
			`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "s"}, "data": {"password": "plain text!"}}`,
			[]want{{diag.CodeInvalidValue, "", "put plain text in stringData"}},
		},
		{
			"TLS Secret without key",
			`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "tls"}, "type": "kubernetes.io/tls", "stringData": {"tls.crt": "x"}}`,
			[]want{{diag.CodeMissingField, "data[tls.key]", "Required value"}},
		},
		{
			"invalid names",
			`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "My_Config", "namespace": "Prod"}}`,
			[]want{
				{diag.CodeInvalidValue, "metadata.name", "RFC 1123 subdomain"},
				{diag.CodeInvalidValue, "metadata.namespace", "RFC 1123 label"},
			},
		},
		{
			"Service name must start with a letter",
			`{"apiVersion": "v1", "kind": "Service", "metadata": {"name": "1web"}, "spec": {"ports": [{"port": 80}]}}`,
			[]want{{diag.CodeInvalidValue, "metadata.name", "DNS-1035 label"}},
		},
		{
			"missing name",
			`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"generateName": "cfg-"}}`,
			[]want{{diag.CodeMissingField, "metadata.name", "generateName is not supported"}},
		},
		{
			"bad labels on a built-in kind",
			`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "cfg", "labels": {"my label": "x", "version": 2}}}`,
			[]want{
				{diag.CodeInvalidValue, "metadata.labels", "name part must consist of alphanumeric"},
				{diag.CodeInvalidValue, "metadata.labels", "expected a string, got number"},
			},
		},
		{
			"bad labels on a custom resource",
			`{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": {"name": "w", "labels": {"version": 2}}}`,
			[]want{{diag.CodeInvalidValue, "metadata.labels[version]", "must be a string"}},
		},
		{
			"CRD name must be plural.group",
			`{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": {"name": "widget"}, "spec": {"group": "example.com", "names": {"kind": "Widget", "plural": "widgets"}}}`,
			[]want{{diag.CodeInvalidValue, "metadata.name", `"widgets.example.com"`}},
		},
		{
			"no apiVersion, kind or metadata",
			`{"spec": {}}`,
			[]want{
				{diag.CodeMissingField, "apiVersion", "Required value"},
				{diag.CodeMissingField, "kind", "Required value"},
				{diag.CodeMissingField, "metadata", "Required value"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertDiagnostics(t, validateOne(t, tc.js), tc.wants...)
		})
	}
}

func TestValidate_PointsAtOrigin(t *testing.T) {
	resources := []manifest.Resource{
		obj(t, `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "ok"}}`),
		obj(t, `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "bad"}, "spec": {}}`),
	}
	origins := [][]diag.Frame{
		{{File: "main.ct", Line: 3, Column: 1}},
		{{File: "lib/cm.ct", Line: 2, Column: 3}, {File: "main.ct", Line: 7, Column: 1}},
	}

	got := validate.Resources(resources, func(i int) []diag.Frame { return origins[i] })

	require.Len(t, got, 1)
	assert.Equal(t, "lib/cm.ct", got[0].File)
	assert.Equal(t, 2, got[0].Line)
	assert.Equal(t, []diag.Frame{{File: "main.ct", Line: 7, Column: 1}}, got[0].Stack)
	assert.Equal(t, "unknown field", got[0].Message)
	assert.Equal(t, `ConfigMap "bad"`, got[0].Resource)
	assert.NotEmpty(t, got[0].Hint)
}

func TestValidate_DoesNotMutateInput(t *testing.T) {
	res := obj(t, `{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "web"}, "spec": {"replicas": "3"}}`)
	before, _ := json.Marshal(res)

	validate.Resources([]manifest.Resource{res}, nil)

	after, _ := json.Marshal(res)
	assert.JSONEq(t, string(before), string(after))
}

package validate

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// checkTyped applies the API server's admission rules that strict decoding
// can't see. Only rules the server enforces belong here.
func checkTyped(obj runtime.Object) field.ErrorList {
	spec := field.NewPath("spec")
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return checkController(o.Spec.Selector, &o.Spec.Template, spec)
	case *appsv1.StatefulSet:
		// The API server adds volumeClaimTemplates as pod volumes, so
		// containers may mount them by name.
		var claims []string
		for _, c := range o.Spec.VolumeClaimTemplates {
			claims = append(claims, c.Name)
		}
		return checkController(o.Spec.Selector, &o.Spec.Template, spec, claims...)
	case *appsv1.DaemonSet:
		return checkController(o.Spec.Selector, &o.Spec.Template, spec)
	case *appsv1.ReplicaSet:
		return checkController(o.Spec.Selector, &o.Spec.Template, spec)
	case *batchv1.Job:
		return checkJobTemplate(&o.Spec.Template, spec.Child("template"))
	case *batchv1.CronJob:
		var errs field.ErrorList
		if o.Spec.Schedule == "" {
			errs = append(errs, field.Required(spec.Child("schedule"), "e.g. \"0 3 * * *\""))
		}
		return append(errs, checkJobTemplate(&o.Spec.JobTemplate.Spec.Template, spec.Child("jobTemplate", "spec", "template"))...)
	case *corev1.Pod:
		return checkPodSpec(&o.Spec, spec)
	case *corev1.Service:
		return checkService(o, spec)
	case *networkingv1.Ingress:
		return checkIngress(o, spec)
	case *autoscalingv2.HorizontalPodAutoscaler:
		return checkHPA(o, spec)
	case *policyv1.PodDisruptionBudget:
		if o.Spec.MinAvailable != nil && o.Spec.MaxUnavailable != nil {
			return field.ErrorList{field.Invalid(spec, "", "minAvailable and maxUnavailable cannot be both set")}
		}
	case *corev1.ConfigMap:
		return checkDataKeys(field.NewPath("data"), keysOf(o.Data), field.NewPath("binaryData"), keysOfBytes(o.BinaryData))
	case *corev1.Secret:
		return checkSecret(o)
	}
	return nil
}

// checkController covers Deployment, StatefulSet, DaemonSet and ReplicaSet.
func checkController(selector *metav1.LabelSelector, template *corev1.PodTemplateSpec, spec *field.Path, extraVolumes ...string) field.ErrorList {
	var errs field.ErrorList
	switch {
	case selector == nil:
		errs = append(errs, field.Required(spec.Child("selector"), "e.g. matchLabels: { app: <name> }, matching spec.template.metadata.labels"))
	case len(selector.MatchLabels)+len(selector.MatchExpressions) == 0:
		errs = append(errs, field.Invalid(spec.Child("selector"), selector, "empty selector is invalid"))
	default:
		matches, err := labelSelectorMatches(selector, template.Labels)
		if err != nil {
			errs = append(errs, field.Invalid(spec.Child("selector"), selector, err.Error()))
		} else if !matches {
			errs = append(errs, field.Invalid(spec.Child("template", "metadata", "labels"), template.Labels,
				"`selector` does not match template `labels`"))
		}
	}

	podSpec := spec.Child("template", "spec")
	if p := template.Spec.RestartPolicy; p != "" && p != corev1.RestartPolicyAlways {
		errs = append(errs, field.NotSupported(podSpec.Child("restartPolicy"), p, []string{string(corev1.RestartPolicyAlways)}))
	}
	return append(errs, checkPodSpec(&template.Spec, podSpec, extraVolumes...)...)
}

func checkJobTemplate(template *corev1.PodTemplateSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	podSpec := path.Child("spec")
	if p := template.Spec.RestartPolicy; p != corev1.RestartPolicyOnFailure && p != corev1.RestartPolicyNever {
		errs = append(errs, field.NotSupported(podSpec.Child("restartPolicy"), p,
			[]string{string(corev1.RestartPolicyOnFailure), string(corev1.RestartPolicyNever)}))
	}
	return append(errs, checkPodSpec(&template.Spec, podSpec)...)
}

func checkPodSpec(spec *corev1.PodSpec, path *field.Path, extraVolumes ...string) field.ErrorList {
	var errs field.ErrorList

	volumes := map[string]bool{}
	for _, name := range extraVolumes {
		volumes[name] = true
	}
	for i, v := range spec.Volumes {
		vp := path.Child("volumes").Index(i)
		if v.Name == "" {
			errs = append(errs, field.Required(vp.Child("name"), ""))
		} else if volumes[v.Name] {
			errs = append(errs, field.Duplicate(vp.Child("name"), v.Name))
		}
		volumes[v.Name] = true
		// No source at all is fine: the API server defaults it to emptyDir.
		if countSet(v.VolumeSource) > 1 {
			errs = append(errs, field.Forbidden(vp, "may not specify more than 1 volume type"))
		}
	}

	if len(spec.Containers) == 0 {
		errs = append(errs, field.Required(path.Child("containers"), "at least one container"))
	}
	names := map[string]bool{}
	errs = append(errs, checkContainers(spec.InitContainers, path.Child("initContainers"), names, volumes)...)
	errs = append(errs, checkContainers(spec.Containers, path.Child("containers"), names, volumes)...)
	return errs
}

func checkContainers(containers []corev1.Container, path *field.Path, names, volumes map[string]bool) field.ErrorList {
	var errs field.ErrorList
	for i, c := range containers {
		cp := path.Index(i)
		switch {
		case c.Name == "":
			errs = append(errs, field.Required(cp.Child("name"), ""))
		case names[c.Name]:
			errs = append(errs, field.Duplicate(cp.Child("name"), c.Name))
		default:
			for _, msg := range utilvalidation.IsDNS1123Label(c.Name) {
				errs = append(errs, field.Invalid(cp.Child("name"), c.Name, msg))
			}
		}
		names[c.Name] = true
		if c.Image == "" {
			errs = append(errs, field.Required(cp.Child("image"), ""))
		}
		for j, p := range c.Ports {
			pp := cp.Child("ports").Index(j)
			for _, msg := range utilvalidation.IsValidPortNum(int(p.ContainerPort)) {
				errs = append(errs, field.Invalid(pp.Child("containerPort"), p.ContainerPort, msg))
			}
			if p.Name != "" {
				for _, msg := range utilvalidation.IsValidPortName(p.Name) {
					errs = append(errs, field.Invalid(pp.Child("name"), p.Name, msg))
				}
			}
		}
		for j, m := range c.VolumeMounts {
			if m.Name != "" && !volumes[m.Name] {
				errs = append(errs, field.NotFound(cp.Child("volumeMounts").Index(j).Child("name"), m.Name))
			}
		}
	}
	return errs
}

func checkService(svc *corev1.Service, spec *field.Path) field.ErrorList {
	var errs field.ErrorList
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		if svc.Spec.ExternalName == "" {
			errs = append(errs, field.Required(spec.Child("externalName"), ""))
		}
		return errs
	}
	headless := svc.Spec.ClusterIP == corev1.ClusterIPNone ||
		(len(svc.Spec.ClusterIPs) > 0 && svc.Spec.ClusterIPs[0] == corev1.ClusterIPNone)
	if len(svc.Spec.Ports) == 0 && !headless {
		errs = append(errs, field.Required(spec.Child("ports"), "e.g. ports: [{ port: 80, targetPort: 8080 }]"))
	}
	names := map[string]bool{}
	for i, p := range svc.Spec.Ports {
		pp := spec.Child("ports").Index(i)
		for _, msg := range utilvalidation.IsValidPortNum(int(p.Port)) {
			errs = append(errs, field.Invalid(pp.Child("port"), p.Port, msg))
		}
		if len(svc.Spec.Ports) > 1 && p.Name == "" {
			errs = append(errs, field.Required(pp.Child("name"), "required when the Service has more than one port"))
		}
		if p.Name != "" {
			if names[p.Name] {
				errs = append(errs, field.Duplicate(pp.Child("name"), p.Name))
			}
			names[p.Name] = true
		}
		errs = append(errs, checkPortRef(p.TargetPort, pp.Child("targetPort"), true)...)
	}
	return errs
}

// checkPortRef validates a port given as a number or an IANA name. An
// optional port may be 0 or "" (the API server defaults it).
func checkPortRef(port intstr.IntOrString, path *field.Path, optional bool) field.ErrorList {
	var errs field.ErrorList
	switch {
	case optional && ((port.Type == intstr.Int && port.IntVal == 0) || (port.Type == intstr.String && port.StrVal == "")):
	case port.Type == intstr.Int:
		for _, msg := range utilvalidation.IsValidPortNum(int(port.IntVal)) {
			errs = append(errs, field.Invalid(path, port.IntVal, msg))
		}
	default:
		for _, msg := range utilvalidation.IsValidPortName(port.StrVal) {
			errs = append(errs, field.Invalid(path, port.StrVal, msg))
		}
	}
	return errs
}

func checkIngress(ing *networkingv1.Ingress, spec *field.Path) field.ErrorList {
	var errs field.ErrorList
	if ing.Spec.DefaultBackend == nil && len(ing.Spec.Rules) == 0 {
		errs = append(errs, field.Invalid(spec, "", "either `defaultBackend` or `rules` must be specified"))
	}
	if ing.Spec.DefaultBackend != nil {
		errs = append(errs, checkIngressBackend(ing.Spec.DefaultBackend, spec.Child("defaultBackend"))...)
	}
	for i, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for j, p := range rule.HTTP.Paths {
			pp := spec.Child("rules").Index(i).Child("http", "paths").Index(j)
			if p.PathType == nil {
				errs = append(errs, field.Required(pp.Child("pathType"), "Prefix, Exact or ImplementationSpecific"))
			}
			errs = append(errs, checkIngressBackend(&p.Backend, pp.Child("backend"))...)
		}
	}
	return errs
}

func checkIngressBackend(b *networkingv1.IngressBackend, path *field.Path) field.ErrorList {
	switch {
	case b.Service != nil && b.Resource != nil:
		return field.ErrorList{field.Invalid(path, "", "cannot set both service and resource")}
	case b.Resource != nil:
		return nil
	case b.Service == nil:
		return field.ErrorList{field.Required(path.Child("service"), "e.g. service: { name: <service>, port: { number: 80 } }")}
	}
	var errs field.ErrorList
	sp := path.Child("service")
	if b.Service.Name == "" {
		errs = append(errs, field.Required(sp.Child("name"), ""))
	}
	port := b.Service.Port
	switch {
	case port.Name != "" && port.Number != 0:
		errs = append(errs, field.Invalid(sp.Child("port"), port, "cannot set both port name and port number"))
	case port.Name != "":
		for _, msg := range utilvalidation.IsValidPortName(port.Name) {
			errs = append(errs, field.Invalid(sp.Child("port", "name"), port.Name, msg))
		}
	default:
		for _, msg := range utilvalidation.IsValidPortNum(int(port.Number)) {
			errs = append(errs, field.Invalid(sp.Child("port", "number"), port.Number, msg))
		}
	}
	return errs
}

func checkHPA(hpa *autoscalingv2.HorizontalPodAutoscaler, spec *field.Path) field.ErrorList {
	var errs field.ErrorList
	if hpa.Spec.ScaleTargetRef.Kind == "" {
		errs = append(errs, field.Required(spec.Child("scaleTargetRef", "kind"), ""))
	}
	if hpa.Spec.ScaleTargetRef.Name == "" {
		errs = append(errs, field.Required(spec.Child("scaleTargetRef", "name"), ""))
	}
	if hpa.Spec.MaxReplicas < 1 {
		errs = append(errs, field.Invalid(spec.Child("maxReplicas"), hpa.Spec.MaxReplicas, "must be greater than or equal to 1"))
	}
	if min := hpa.Spec.MinReplicas; min != nil && *min > hpa.Spec.MaxReplicas {
		errs = append(errs, field.Invalid(spec.Child("maxReplicas"), hpa.Spec.MaxReplicas, "must be greater than or equal to `minReplicas`"))
	}
	return errs
}

func checkSecret(s *corev1.Secret) field.ErrorList {
	keys := keysOfBytes(s.Data)
	stringKeys := keysOf(s.StringData)
	errs := checkDataKeys(field.NewPath("data"), keys, field.NewPath("stringData"), nil)
	errs = append(errs, checkDataKeys(field.NewPath("stringData"), stringKeys, nil, nil)...)

	present := map[string]bool{}
	for _, k := range append(keys, stringKeys...) {
		present[k] = true
	}
	var required []string
	switch s.Type {
	case corev1.SecretTypeTLS:
		required = []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey}
	case corev1.SecretTypeDockerConfigJson:
		required = []string{corev1.DockerConfigJsonKey}
	case corev1.SecretTypeBasicAuth:
		if !present[corev1.BasicAuthUsernameKey] && !present[corev1.BasicAuthPasswordKey] {
			required = []string{corev1.BasicAuthUsernameKey}
		}
	}
	for _, k := range required {
		if !present[k] {
			errs = append(errs, field.Required(field.NewPath("data").Key(k), fmt.Sprintf("needed by type %s", s.Type)))
		}
	}
	return errs
}

func checkDataKeys(path *field.Path, keys []string, otherPath *field.Path, otherKeys []string) field.ErrorList {
	var errs field.ErrorList
	other := map[string]bool{}
	for _, k := range otherKeys {
		other[k] = true
	}
	for _, k := range keys {
		for _, msg := range utilvalidation.IsConfigMapKey(k) {
			errs = append(errs, field.Invalid(path.Key(k), k, msg))
		}
		if other[k] {
			errs = append(errs, field.Invalid(otherPath.Key(k), k, "duplicate of key present in "+path.String()))
		}
	}
	return errs
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return sortedStrings(keys)
}

func keysOfBytes(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return sortedStrings(keys)
}

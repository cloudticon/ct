package dev

import (
	"fmt"
	"sort"

	"github.com/cloudticon/ct/pkg/engine"
)

var workloadKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"ReplicaSet":  true,
	"Job":         true,
}

type indexedResource struct {
	resource engine.Resource
	conflict bool
}

// ResolveSelectors auto-resolves label selectors for targets that don't have
// an explicit selector by looking up the matching workload in the rendered resources.
func ResolveSelectors(targets []Target, resources []engine.Resource) error {
	resourceMap := indexWorkloads(resources)
	for i := range targets {
		if targets[i].Selector != nil {
			continue
		}
		selector, err := resolveSelector(targets[i].Name, resourceMap)
		if err != nil {
			return err
		}
		targets[i].Selector = selector
	}
	return nil
}

// UniqueWorkloadNames returns names of workloads that have no name conflicts.
// Used by ct types --dev to generate the CtResource type.
func UniqueWorkloadNames(resources []engine.Resource) []string {
	index := indexWorkloads(resources)
	var names []string
	for name, entry := range index {
		if !entry.conflict {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func indexWorkloads(resources []engine.Resource) map[string]indexedResource {
	m := make(map[string]indexedResource)
	for _, r := range resources {
		kind, _ := r["kind"].(string)
		if !workloadKinds[kind] {
			continue
		}
		name := getResourceName(r)
		if name == "" {
			continue
		}
		if _, exists := m[name]; exists {
			m[name] = indexedResource{resource: r, conflict: true}
		} else {
			m[name] = indexedResource{resource: r}
		}
	}
	return m
}

func resolveSelector(name string, workloads map[string]indexedResource) (map[string]string, error) {
	entry, ok := workloads[name]
	if !ok {
		return nil, fmt.Errorf(
			"resource %q not found among workloads in main.ct output; "+
				"use selector: {...} for external resources or non-workload types", name,
		)
	}
	if entry.conflict {
		return nil, fmt.Errorf(
			"resource %q is ambiguous (multiple workloads with same name); "+
				"use selector: {...} to specify explicitly", name,
		)
	}
	return extractSelector(entry.resource)
}

func extractSelector(res engine.Resource) (map[string]string, error) {
	return getNestedStringMap(res, "spec", "selector", "matchLabels")
}

func getNestedStringMap(obj map[string]interface{}, keys ...string) (map[string]string, error) {
	current := obj
	for _, k := range keys[:len(keys)-1] {
		next, ok := current[k].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("missing path %v in resource", keys)
		}
		current = next
	}
	raw, ok := current[keys[len(keys)-1]].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("selector not found at path %v", keys)
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}
	return result, nil
}

func getResourceName(r engine.Resource) string {
	meta, ok := r["metadata"].(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := meta["name"].(string)
	return name
}

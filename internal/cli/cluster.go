package cli

import "github.com/cloudticon/ct/pkg/k8s"

// newClusterFn is the single seam for constructing a k8s.Cluster from CLI
// flags. Tests override this to inject k8stest.NewFake() and exercise
// commands without a real cluster. Production calls return the live adapter.
var newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
	return k8s.NewLiveCluster(k8s.LiveOpts{
		KubeContext:      kubeContext,
		DefaultNamespace: namespace,
	})
}

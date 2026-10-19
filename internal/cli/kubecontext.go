package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// kubeContextInfo is what ct knows about the kubeconfig before it touches a
// cluster: the contexts, the current one and the API server of each.
type kubeContextInfo struct {
	Current  string
	Contexts []string
	Servers  map[string]string // context name -> API server address
}

// Seams for tests: terminal detection, in-cluster detection and kubeconfig
// loading.
var (
	stdinIsTerminalFn = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	inClusterFn       = func() bool { _, err := rest.InClusterConfig(); return err == nil }
	loadKubeContextFn = loadKubeContextInfo
)

func loadKubeContextInfo() (kubeContextInfo, error) {
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return kubeContextInfo{}, err
	}
	info := kubeContextInfo{Current: raw.CurrentContext, Servers: map[string]string{}}
	for name, c := range raw.Contexts {
		info.Contexts = append(info.Contexts, name)
		if cl, ok := raw.Clusters[c.Cluster]; ok {
			info.Servers[name] = cl.Server
		}
	}
	sort.Strings(info.Contexts)
	return info, nil
}

// requireKubeContext guards commands that change a cluster (apply, delete,
// dev). Without --context it refuses when stdin is not a terminal, so an agent
// or CI job names its cluster; in a terminal it uses the default context and
// prints which cluster that is before anything changes. Inside a cluster
// (in-cluster configuration) no context is needed.
func requireKubeContext(cmd *cobra.Command, kubeContext string) error {
	if kubeContext != "" || inClusterFn() {
		return nil
	}
	info, infoErr := loadKubeContextFn()
	if !stdinIsTerminalFn() {
		return usageError(cmd, &contextRequiredError{info: info, infoErr: infoErr})
	}
	if infoErr != nil || info.Current == "" {
		return nil
	}
	describeContext(cmd.ErrOrStderr(), info)
	return nil
}

func describeContext(w io.Writer, info kubeContextInfo) {
	if server := info.Servers[info.Current]; server != "" {
		fmt.Fprintf(w, "using kubeconfig context %q (API server %s)\n", info.Current, server)
		return
	}
	fmt.Fprintf(w, "using kubeconfig context %q\n", info.Current)
}

type contextRequiredError struct {
	info    kubeContextInfo
	infoErr error
}

func (e *contextRequiredError) Error() string {
	var b strings.Builder
	b.WriteString("--context is required when stdin is not a terminal; no cluster was changed. ")
	switch {
	case e.infoErr != nil:
		fmt.Fprintf(&b, "could not read the kubeconfig: %v", e.infoErr)
	case len(e.info.Contexts) == 0:
		b.WriteString("the kubeconfig has no contexts")
	default:
		fmt.Fprintf(&b, "contexts in the kubeconfig: %s", strings.Join(e.info.Contexts, ", "))
		if e.info.Current != "" {
			fmt.Fprintf(&b, " (current: %s)", e.info.Current)
		}
	}
	return b.String()
}

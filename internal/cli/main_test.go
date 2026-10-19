package cli

import (
	"os"
	"testing"
)

// Tests run without a terminal; default to the terminal case so commands run
// without --context, and the non-terminal tests opt in explicitly.
func TestMain(m *testing.M) {
	stdinIsTerminalFn = func() bool { return true }
	inClusterFn = func() bool { return false }
	loadKubeContextFn = func() (kubeContextInfo, error) { return kubeContextInfo{}, nil }
	os.Exit(m.Run())
}

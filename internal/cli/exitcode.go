package cli

import (
	"errors"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/spf13/cobra"
)

// Exit codes. An agent decides what to fix from the code alone:
// 1 - the .ct code, values or manifests are wrong; 2 - ct was called wrongly;
// 3 - the cluster is unreachable, refused the request or failed.
const (
	ExitCodeError   = 1
	ExitCodeUsage   = 2
	ExitCodeCluster = 3
)

// exitError carries the process exit code of a failure.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// ExitCode maps err to the process exit code.
func ExitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitCodeError
}

// usageError marks err as wrong use of ct (arguments, flags, commands) and
// adds the usage line and an example, so the next call can be right.
func usageError(cmd *cobra.Command, err error) error {
	var ee *exitError
	if err == nil || errors.As(err, &ee) {
		return err
	}
	var hint strings.Builder
	hint.WriteString("usage: " + cmd.UseLine())
	if ex := strings.TrimSpace(cmd.Example); ex != "" {
		hint.WriteString("; example: " + strings.Join(strings.Fields(strings.ReplaceAll(ex, "\n", " ; ")), " "))
	}
	hint.WriteString("; see `" + cmd.CommandPath() + " --help`")
	return &exitError{code: ExitCodeUsage, err: diag.List{{
		Code:    diag.CodeUsage,
		Message: err.Error(),
		Hint:    hint.String(),
	}}}
}

// clusterError marks err as a cluster problem (connection, authorization,
// rejection). Code problems that already are diagnostics keep exit code 1,
// except namespace-not-found, which the cluster reported.
func clusterError(err error) error {
	var ee *exitError
	if err == nil || errors.As(err, &ee) {
		return err
	}
	var list diag.List
	if errors.As(err, &list) {
		for _, d := range list {
			if d.Code != diag.CodeNamespaceNotFound {
				return err
			}
		}
	}
	return &exitError{code: ExitCodeCluster, err: err}
}

// usageArgs wraps a positional-argument validator so its failure is a usage error.
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return usageError(cmd, v(cmd, args))
	}
}

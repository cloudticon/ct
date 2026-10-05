//go:build !windows

package cli

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// The dev session's graceful shutdown (stop port-forwards, restore the
// terminal, flush) hangs off ctx, but nothing cancelled ctx on Ctrl+C or
// SIGTERM: the process was simply killed, e.g. with the TTY left in raw mode.
func TestRunDev_CancelsContextOnSignal(t *testing.T) {
	origRunner := runDevMode
	t.Cleanup(func() { runDevMode = origRunner })

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		runDevMode = func(ctx context.Context, _ dev.RunOpts) error {
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				return errors.New("context was not cancelled by " + sig.String())
			}
		}
		require.NoError(t, runDev(&cobra.Command{}, devOpts{}))
	}
}

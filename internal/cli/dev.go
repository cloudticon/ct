package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/spf13/cobra"
)

type devOpts struct {
	envFile         string
	context         string
	releaseName     string
	delete          bool
	createNamespace bool
}

var runDevMode = dev.Run

func newDevCmd() *cobra.Command {
	var opts devOpts

	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Start development mode from dev.ct configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.envFile, "env-file", ".env", "path to .env file (empty to skip)")
	cmd.Flags().StringVar(&opts.context, "context", "", "kubeconfig context")
	cmd.Flags().StringVar(&opts.releaseName, "name", "dev", "release name for inventory and labels")
	cmd.Flags().BoolVar(&opts.delete, "delete", false, "delete resources from previous dev session")
	cmd.Flags().BoolVar(&opts.createNamespace, "create-namespace", true, "create namespace if it does not exist")

	return cmd
}

func init() {
	rootCmd.AddCommand(newDevCmd())
}

func runDev(cmd *cobra.Command, opts devOpts) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	ctx, stop := devSignalContext(cmd.Context())
	defer stop()

	if err := runDevMode(ctx, dev.RunOpts{
		Dir:             dir,
		EnvFile:         opts.envFile,
		KubeCtx:         opts.context,
		ReleaseName:     opts.releaseName,
		Delete:          opts.delete,
		CreateNamespace: opts.createNamespace,
		Stdin:           cmd.InOrStdin(),
		Stdout:          cmd.OutOrStdout(),
		Stderr:          cmd.ErrOrStderr(),
	}); err != nil {
		return fmt.Errorf("running dev mode: %w", err)
	}

	return nil
}

// devSignalContext cancels the dev session on Ctrl+C or SIGTERM so it can
// shut down gracefully (stop port-forwards and syncs, restore the terminal).
// Once the context is cancelled the default signal behavior is restored, so
// a second Ctrl+C still kills a session that does not stop.
func devSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

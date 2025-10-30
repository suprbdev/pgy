package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/suprbdev/pgy/internal/cli"
)

func main() {
    root := &cobra.Command{
        Use:   "pgy",
        Short: "pgy is a forward-only PostgreSQL migration tool",
        RunE: func(cmd *cobra.Command, args []string) error {
            return cmd.Help()
        },
        SilenceUsage:  true,
        SilenceErrors: true,
    }

    ctx := context.Background()

    cli.AttachGlobalFlags(root)
    cli.RegisterCommands(ctx, root)

    if err := root.Execute(); err != nil {
        if code, ok := cli.AsExitCode(err); ok {
            os.Exit(code)
        }
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }
}



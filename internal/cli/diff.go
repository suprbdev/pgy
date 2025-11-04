package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/schema"
)

func cmdDiff() *cobra.Command {
    var unsafe bool
    var jsonOut bool
    var fromEmpty bool
    c := &cobra.Command{
        Use:   "diff",
        Short: "Generate SQL diff between YAML schema and live DB",
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            var conn interface{ Close() }
            var err error
            var live *diff.Live
            if cfg.DSN == "" {
                if !fromEmpty {
                    return fmt.Errorf("--dsn or PGY_DSN is required (or use --from-empty)")
                }
                live = &diff.Live{Schemas: map[string]bool{}, Tables: map[string]*diff.LiveTable{}}
            } else {
                pool, err2 := db.Connect(ctx, cfg.DSN)
                if err2 != nil {
                    return err2
                }
                conn = pool
                defer conn.Close()
                live, err = diff.Introspect(ctx, pool)
                if err != nil {
                    return err
                }
            }

            // load and merge schemas in order
            schemaPaths := make([]string, 0, len(cfg.Schemas))
            for _, s := range cfg.Schemas {
                schemaPaths = append(schemaPaths, filepath.Join(cfg.SchemaRoot, s))
            }
            compiled, err := schema.LoadAndMerge(schemaPaths)
            if err != nil {
                return err
            }
            plan := diff.Plan(live, compiled, unsafe)
            sql := diff.Render(plan)

            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                _ = enc.Encode(plan.Summary())
            }

            if len(sql) == 0 {
                if !cfg.Quiet {
                    fmt.Println("no changes detected")
                }
                return nil
            }
            if err := os.WriteFile(cfg.BufferPath, []byte(sql), 0o644); err != nil {
                return err
            }
            if !cfg.Quiet {
                fmt.Printf("wrote diff to %s (%d bytes)\n", cfg.BufferPath, len(sql))
            }
            // exit code 2 for pending changes
            return &exitCodeError{code: 2}
        },
    }
    c.Flags().BoolVar(&unsafe, "unsafe", false, "Allow destructive changes (drops/alters)")
    c.Flags().BoolVar(&jsonOut, "json", false, "Output machine-readable diff summary")
    c.Flags().BoolVar(&fromEmpty, "from-empty", false, "Generate diff from an empty database (no DSN required)")
    return c
}

type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit %d", e.code) }



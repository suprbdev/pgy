package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/group"
    "github.com/suprbdev/pgy/internal/schema"
)

func cmdDiff() *cobra.Command {
    var unsafe bool
    var jsonOut bool
    var fromEmpty bool
    var split string
    c := &cobra.Command{
        Use:   "diff",
        Short: "Generate SQL diff between YAML schema and live DB",
        Long: `Generate SQL diff between YAML schema and live DB.

Exit codes:
  0  No changes detected (schema matches live DB)
  2  Changes detected and written to buffer file (not an error)
  1  Unexpected error`,
        Args: cobra.NoArgs,
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
                live = &diff.Live{
                    Schemas: map[string]bool{}, 
                    Tables: map[string]*diff.LiveTable{},
                    Types: map[string]bool{},
                    Functions: map[string]bool{},
                    Extensions: map[string]bool{},
                }
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
            // fail loudly on dependsOn cycles: the plan's entity order would
            // not be dependency-correct
            if _, err := schema.TopologicalSort(compiled); err != nil {
                return err
            }
            var sql string
            var summary any
            groups := 0
            if split != "" {
                g, err := group.Split(schemaPaths, split)
                if err != nil {
                    return err
                }
                steps, err := group.Plan(live, schemaPaths, g, unsafe)
                if err != nil {
                    return err
                }
                sql = group.Render(steps)
                groups = len(steps)
                summary = splitSummary(steps)
            } else {
                plan := diff.Plan(live, compiled, unsafe)
                sql = diff.Render(plan)
                summary = plan.Summary()
            }

            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                _ = enc.Encode(summary)
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
                if split != "" {
                    fmt.Printf("wrote diff to %s (%d bytes, %d groups)\n", cfg.BufferPath, len(sql), groups)
                } else {
                    fmt.Printf("wrote diff to %s (%d bytes)\n", cfg.BufferPath, len(sql))
                }
            }
            // exit code 2 for pending changes
            return &exitCodeError{code: 2}
        },
    }
    c.Flags().BoolVar(&unsafe, "unsafe", false, "Allow destructive changes (drops/alters)")
    c.Flags().BoolVar(&jsonOut, "json", false, "Output machine-readable diff summary")
    c.Flags().BoolVar(&fromEmpty, "from-empty", false, "Generate diff from an empty database (no DSN required)")
    c.Flags().StringVar(&split, "split", "", "Split the diff into one migration group per schema file (file) or directory (dir), ordered by dependencies")
    c.Flags().Lookup("split").NoOptDefVal = group.ModeFile
    return c
}

func splitSummary(steps []group.Step) map[string]any {
    total := map[string]int{"creates": 0, "alters": 0, "drops": 0}
    groups := make([]map[string]any, 0, len(steps))
    for _, s := range steps {
        g := map[string]any{"name": s.Name}
        for k, v := range s.Plan.Summary() {
            g[k] = v
            total[k] += v
        }
        groups = append(groups, g)
    }
    out := map[string]any{"groups": groups}
    for k, v := range total {
        out[k] = v
    }
    return out
}

type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit %d", e.code) }



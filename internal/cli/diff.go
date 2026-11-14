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
    var prune bool
    var jsonOut bool
    var fromEmpty bool
    var split string
    var noWarnings bool
    c := &cobra.Command{
        Use:   "diff",
        Short: "Generate SQL diff between YAML schema and live DB",
        Long: `Generate SQL diff between YAML schema and live DB.

Statements that lock or rewrite an existing table (an index built without
concurrently, a foreign key or check added without notValid, SET NOT NULL, a
rewriting type change, SET LOGGED/UNLOGGED, an added column whose value is
computed per row) are reported on stderr. Use --no-warnings to silence them.

With --buffer - the SQL is printed to stdout instead of written to the
buffer file, and status messages go to stderr.

Exit codes:
  0  No changes detected (schema matches live DB)
  2  Changes detected and written to the buffer file or stdout (not an error)
  1  Unexpected error`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            if prune && !unsafe {
                return fmt.Errorf("--prune drops objects and requires --unsafe")
            }
            toStdout := cfg.BufferPath == "-"
            if toStdout && (jsonOut || cfg.JSON) {
                return fmt.Errorf("--buffer - prints the SQL to stdout and cannot be combined with --json")
            }
            migSchema, migTable := cfg.migrationsTable()
            opts := diff.Options{Unsafe: unsafe, Prune: prune, MigrationsTable: migSchema + "." + migTable}
            schemaPaths, err := resolveSchemaPaths(cfg.SchemaRoot, cfg.Schemas)
            if err != nil {
                return err
            }
            var conn interface{ Close() }
            var live *diff.Live
            if fromEmpty || cfg.DSN == "" {
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
                steps, err := group.Plan(live, schemaPaths, g, opts)
                if err != nil {
                    return err
                }
                sql = group.Render(steps)
                groups = len(steps)
                summary = splitSummary(steps)
                if !noWarnings {
                    for _, s := range steps {
                        for _, w := range s.Plan.Warnings { fmt.Fprintf(os.Stderr, "warning: [%s] %s\n", s.Name, w.Message) }
                    }
                }
            } else {
                plan := diff.PlanWith(live, compiled, opts)
                sql = diff.Render(plan)
                if len(plan.Concurrent) > 0 {
                    sql = group.Render([]group.Step{{Plan: plan}})
                }
                s := map[string]any{}
                for k, v := range plan.Summary() { s[k] = v }
                if len(plan.Warnings) > 0 { s["warnings"] = plan.Warnings }
                summary = s
                if !noWarnings {
                    for _, w := range plan.Warnings { fmt.Fprintln(os.Stderr, "warning:", w.Message) }
                }
            }

            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                _ = enc.Encode(summary)
            }

            if toStdout {
                if len(sql) == 0 {
                    if !cfg.Quiet { fmt.Fprintln(os.Stderr, "no changes detected") }
                    return nil
                }
                if _, err := os.Stdout.WriteString(sql); err != nil { return err }
                return &exitCodeError{code: 2}
            }
            if len(sql) == 0 {
                if err := os.Remove(cfg.BufferPath); err != nil && !os.IsNotExist(err) {
                    return err
                }
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
    c.Flags().BoolVar(&prune, "prune", false, "With --unsafe: drop tables, views, functions, types, sequences, indexes, constraints and triggers that the YAML no longer declares, in every schema it declares objects in. Run it with ALL schema files: objects declared only in files left out are dropped")
    c.Flags().BoolVar(&jsonOut, "json", false, "Output machine-readable diff summary")
    c.Flags().BoolVar(&noWarnings, "no-warnings", false, "Do not report statements that lock or rewrite existing tables")
    c.Flags().BoolVar(&fromEmpty, "from-empty", false, "Generate diff from an empty database (no DSN required)")
    c.Flags().StringVar(&split, "split", "", "Split the diff into one migration group per schema file (file) or directory (dir), ordered by dependencies")
    c.Flags().Lookup("split").NoOptDefVal = group.ModeFile
    return c
}

func resolveSchemaPaths(root string, schemas []string) ([]string, error) {
    out := make([]string, 0, len(schemas))
    for _, s := range schemas {
        p := s
        if !filepath.IsAbs(p) {
            p = filepath.Join(root, s)
        }
        if _, err := os.Stat(p); err != nil {
            if os.IsNotExist(err) {
                return nil, fmt.Errorf("schema file not found: %s", p)
            }
            return nil, err
        }
        out = append(out, p)
    }
    return out, nil
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
        if len(s.Plan.Warnings) > 0 { g["warnings"] = s.Plan.Warnings }
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



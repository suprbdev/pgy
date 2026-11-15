package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sort"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
)

func cmdStatus() *cobra.Command {
    c := &cobra.Command{
        Use:   "status",
        Short: "Show migration status",
        Long: `Show migration status (applied vs pending).

Exit codes:
  0  All migrations applied (nothing pending)
  2  Pending migrations exist (not an error)
  1  Unexpected error`,
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            if cfg.DSN == "" { return fmt.Errorf("--dsn or PGY_DSN is required") }
            pool, err := db.Connect(ctx, cfg.DSN)
            if err != nil { return err }
            defer pool.Close()
            schemaName, tableName := cfg.migrationsTable()
            if err := db.EnsureMigrationsTable(ctx, pool, schemaName, tableName); err != nil { return err }
            applied, err := db.LoadApplied(ctx, pool, schemaName, tableName)
            if err != nil { return err }
            files, err := readMigrationFiles(cfg.MigrationsDir)
            if err != nil { return err }
            warnings := historyWarnings(files, applied)
            pending := []string{}
            last := ""
            for _, f := range files {
                base := filepath.Base(f)
                last = base
                if _, ok := applied[base]; !ok { pending = append(pending, base) }
            }
            current := currentVersion(applied)
            if cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                if err := enc.Encode(map[string]any{"current": current, "last": last, "applied": len(applied), "pending": pending, "warnings": warnings}); err != nil { return err }
            } else {
                for _, w := range warnings { fmt.Fprintln(os.Stderr, "warning:", w) }
                fmt.Printf("current: %s\nlast: %s\npending: %d\n", current, last, len(pending))
            }
            if len(pending) > 0 { return &exitCodeError{code: 2} }
            return nil
        },
    }
    return c
}

func currentVersion(applied map[string]string) string {
    if len(applied) == 0 { return "none" }
    names := make([]string, 0, len(applied))
    for k := range applied { names = append(names, k) }
    sort.Strings(names)
    return names[len(names)-1]
}



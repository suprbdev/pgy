package cli

import (
    "fmt"
    "path/filepath"
    "sort"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
)

func cmdStatus() *cobra.Command {
    c := &cobra.Command{
        Use:   "status",
        Short: "Show migration status",
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            if cfg.DSN == "" { return fmt.Errorf("--dsn or PGY_DSN is required") }
            pool, err := db.Connect(ctx, cfg.DSN)
            if err != nil { return err }
            defer pool.Close()
            schemaName := "pgy"
            tableName := "migrations"
            if err := db.EnsureMigrationsTable(ctx, pool, schemaName, tableName); err != nil { return err }
            applied, err := db.LoadApplied(ctx, pool, schemaName, tableName)
            if err != nil { return err }
            files, err := readMigrationFiles(cfg.MigrationsDir)
            if err != nil { return err }
            pending := 0
            last := ""
            for _, f := range files {
                base := filepath.Base(f)
                last = base
                if _, ok := applied[base]; !ok { pending++ }
            }
            current := currentVersion(applied)
            fmt.Printf("current: %s\nlast: %s\npending: %d\n", current, last, pending)
            if pending > 0 { return &exitCodeError{code: 2} }
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



package cli

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
)

func cmdMarkApplied() *cobra.Command {
    var force bool
    c := &cobra.Command{
        Use:   "mark-applied <migration|path>",
        Short: "Mark migrations as applied up to a specific one",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            target := args[0]
            if cfg.DSN == "" { return fmt.Errorf("--dsn or PGY_DSN is required") }
            pool, err := db.Connect(ctx, cfg.DSN)
            if err != nil { return err }
            defer pool.Close()
            if !force {
                return fmt.Errorf("--force is required to modify migration state")
            }
            files, err := readMigrationFiles(cfg.MigrationsDir)
            if err != nil { return err }
            if len(files) == 0 { return fmt.Errorf("no migrations found in %s", cfg.MigrationsDir) }
            // normalize target to prefix (e.g. 0003 or 0003_name)
            base := strings.TrimSuffix(filepath.Base(target), ".sql")
            until := base
            sel := filterUntil(files, until)
            if len(sel) == 0 { return fmt.Errorf("target not found: %s", target) }
            // clear future rows and insert up to until
            schemaName := "pgy"
            tableName := "migrations"
            if err := db.EnsureMigrationsTable(ctx, pool, schemaName, tableName); err != nil { return err }
            // delete all rows greater than until version
            var untilVersion int
            fmt.Sscanf(base, "%d", &untilVersion)
            _, err = pool.Exec(ctx, fmt.Sprintf("delete from %s.%s where version > $1", dbIdent(schemaName), dbIdent(tableName)), untilVersion)
            if err != nil { return err }
            // insert missing rows up to until
            applied, err := db.LoadApplied(ctx, pool, schemaName, tableName)
            if err != nil { return err }
            for _, f := range sel {
                b, err := os.ReadFile(f)
                if err != nil { return err }
                sum := checksumBody(b)
                if hx := parseChecksumHeader(string(b)); hx != "" && hx != sum {
                    return fmt.Errorf("checksum mismatch for %s", f)
                }
                base := filepath.Base(f)
                if _, ok := applied[base]; ok { continue }
                if err := db.RecordApplied(ctx, pool, schemaName, tableName, base, sum, time.Now().UTC()); err != nil { return err }
                if !cfg.Quiet { fmt.Printf("marked %s applied\n", base) }
            }
            return nil
        },
    }
    c.Flags().BoolVar(&force, "force", false, "Confirm modifying migration state")
    return c
}

func dbIdent(id string) string { return `"` + id + `"` }



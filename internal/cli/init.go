package cli

import (
    "context"
    "fmt"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
)

func cmdInit() *cobra.Command {
    var schema string
    var table string

    c := &cobra.Command{
        Use:   "init",
        Short: "Initialize the migrations table",
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            defSchema, defTable := cfg.migrationsTable()
            if schema == "" {
                schema = defSchema
            }
            if table == "" {
                table = defTable
            }
            if cfg.DSN == "" {
                return fmt.Errorf("--dsn or PGY_DSN is required")
            }
            conn, err := db.Connect(ctx, cfg.DSN)
            if err != nil {
                return err
            }
            defer conn.Close()

            err = db.WithAdvisoryLock(ctx, conn, func(ctx context.Context) error {
                return db.EnsureMigrationsTable(ctx, conn, schema, table)
            })
            if err != nil {
                return err
            }
            if !cfg.Quiet {
                fmt.Printf("initialized %s.%s\n", schema, table)
            }
            return nil
        },
    }
    c.Flags().StringVar(&schema, "schema", "", "Schema for the migrations table (overrides --migrations-table)")
    c.Flags().StringVar(&table, "table", "", "Migrations table name (overrides --migrations-table)")
    return c
}



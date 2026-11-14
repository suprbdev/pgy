package cli

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/pull"
)

func cmdPull() *cobra.Command {
    var out string
    var schemas []string
    var owners, roles, force bool
    c := &cobra.Command{
        Use:   "pull",
        Short: "Generate schema YAML from an existing database",
        Long: `Generate schema YAML from the database at --dsn, so an existing database
can be adopted without writing YAML by hand. The output diffs clean against
the database it was pulled from.

Without --out the YAML is printed as one document. With --out, one file per
schema (<schema>.yml) plus database.yml (extensions, publications, event
triggers, default privileges, roles) is written to that directory.

Objects pgy cannot express (such as functions written in C) are reported
on stderr.`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            if cfg.DSN == "" { return fmt.Errorf("--dsn or PGY_DSN is required") }
            if cmd.Flags().Changed("schemas") { return fmt.Errorf("pull reads no schema files; use --schema to choose which database schemas to pull") }
            pool, err := db.Connect(ctx, cfg.DSN)
            if err != nil { return err }
            defer pool.Close()
            live, err := diff.Introspect(ctx, pool)
            if err != nil { return err }
            if err := diff.IntrospectDetails(ctx, pool, live); err != nil { return err }
            migSchema, migTable := cfg.migrationsTable()
            res, err := pull.Build(live, pull.Options{Schemas: schemas, MigrationsTable: migSchema + "." + migTable, Owners: owners, Roles: roles, Single: out == ""})
            if err != nil { return err }
            for _, w := range res.Warnings { fmt.Fprintln(os.Stderr, "warning:", w) }
            if out == "" {
                for _, f := range res.Files {
                    if _, err := os.Stdout.Write(f.Content); err != nil { return err }
                }
                return nil
            }
            if err := os.MkdirAll(out, 0o755); err != nil { return err }
            for _, f := range res.Files {
                p := filepath.Join(out, f.Name)
                if _, err := os.Stat(p); err == nil && !force {
                    return fmt.Errorf("%s exists; use --force to overwrite", p)
                }
                if err := os.WriteFile(p, f.Content, 0o644); err != nil { return err }
                if !cfg.Quiet { fmt.Printf("wrote %s\n", p) }
            }
            return nil
        },
    }
    c.Flags().StringVar(&out, "out", "", "Directory to write one YAML file per schema into (default: print to stdout)")
    c.Flags().StringSliceVar(&schemas, "schema", nil, "Only pull these schemas (repeatable; default: all)")
    c.Flags().BoolVar(&owners, "owners", false, "Emit owner: for objects not owned by the connecting role")
    c.Flags().BoolVar(&roles, "roles", false, "Emit the cluster's roles")
    c.Flags().BoolVar(&force, "force", false, "Overwrite existing files in --out")
    return c
}

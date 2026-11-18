package cli

import (
    "encoding/json"
    "strings"
    "fmt"
    "os"
    "path/filepath"
    "sort"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/pull"
)

func cmdPull() *cobra.Command {
    var out string
    var schemas, excluded, dataTables []string
    var owners, roles, force, settings bool
    c := &cobra.Command{
        Use:   "pull",
        Short: "Generate schema YAML from an existing database",
        Long: `Generate schema YAML from the database at --dsn, so an existing database
can be adopted without writing YAML by hand. The output diffs clean against
the database it was pulled from.

Without --out the YAML is printed as one document. With --out, one file per
schema (<schema>.yml) plus database.yml (extensions, foreign-data wrappers
and servers, language privileges, publications, event triggers, default
privileges, roles) is written to that directory.

With --schema or --exclude-schema, database.yml holds only what the pulled
schemas need: the extensions installed in them or used by their objects
(and those these require), the publications of their tables, and the event
triggers whose function is in them. Database settings are left out unless
--database-settings is given.

Objects pgy cannot express (functions written in C, operators, operator
classes and families, conversions, base types, text search parsers and
templates, procedural languages and transforms) are reported on stderr;
objects that belong to an extension are not. So are the configuration
tables of the extensions written whose rows pg_dump would keep, unless
--data pulls them. With --out, --json prints the files written and these
warnings as JSON.

The config's external: block leaves out what the application doesn't own:
external schemas with their objects and the extensions installed there,
external relations, objects external roles own, their role entries and
default privileges, and external settings.`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            cfg := FromContext(ctx)
            if cfg.DSN == "" { return fmt.Errorf("--dsn or PGY_DSN is required") }
            if cmd.Flags().Changed("schemas") { return fmt.Errorf("pull reads no schema files; use --schema to choose which database schemas to pull") }
            if out == "" && cmd.Flags().Changed("json") && cfg.JSON { return fmt.Errorf("--json prints a summary of the files written to stdout, so it needs --out") }
            asJSON := cfg.JSON && out != ""
            pool, err := db.Connect(ctx, cfg.DSN)
            if err != nil { return err }
            defer pool.Close()
            live, err := diff.Introspect(ctx, pool)
            if err != nil { return err }
            if err := diff.IntrospectDetails(ctx, pool, live); err != nil { return err }
            migSchema, migTable := cfg.migrationsTable()
            schemas := nameList(schemas)
            data := map[string][]map[string]any{}
            for _, t := range nameList(dataTables) {
                fq := t
                if !strings.Contains(fq, ".") { fq = "public." + fq }
                if live.Tables[fq] == nil { return fmt.Errorf("--data %s: no such table", t) }
                rows, err := diff.TableRowsWhere(ctx, pool, fq, live.Tables[fq].Partitioned, live.ExtensionConfig[fq])
                if err != nil { return err }
                data[fq] = rows
            }
            configTables := []string{}
            for fq := range live.ExtensionConfig {
                if _, pulled := data[fq]; !pulled && !cfg.External.HasRelation(fq) { configTables = append(configTables, fq) }
            }
            sort.Strings(configTables)
            configRows, err := diff.ConfigRowCounts(ctx, pool, live, configTables)
            if err != nil { fmt.Fprintln(os.Stderr, "warning: the rows of extension configuration tables were not counted:", err) }
            res, err := pull.Build(live, pull.Options{Schemas: schemas, ExcludeSchemas: nameList(excluded), DatabaseSettings: settings, MigrationsTable: migSchema + "." + migTable, Owners: owners, OwnerKinds: cfg.Policy.RequireOwners, Roles: roles, Single: out == "", Data: data, External: cfg.External, ConfigRows: configRows})
            if err != nil { return err }
            for _, w := range res.Warnings { fmt.Fprintln(os.Stderr, "warning:", w) }
            strict := &strictFindings{}
            if cfg.failsOn("unsupported") {
                for _, w := range res.Warnings { strict.add("unsupported", w) }
            }
            if out == "" {
                for _, f := range res.Files {
                    if _, err := os.Stdout.Write(f.Content); err != nil { return err }
                }
                return strict.err(nil)
            }
            if err := os.MkdirAll(out, 0o755); err != nil { return err }
            written := []string{}
            for _, f := range res.Files {
                p := filepath.Join(out, f.Name)
                if _, err := os.Stat(p); err == nil && !force {
                    return fmt.Errorf("%s exists; use --force to overwrite", p)
                }
                if err := os.WriteFile(p, f.Content, 0o644); err != nil { return err }
                written = append(written, p)
                if !cfg.Quiet && !asJSON { fmt.Printf("wrote %s\n", p) }
            }
            if asJSON {
                type warning struct {
                    Kind    string `json:"kind"`
                    Message string `json:"message"`
                }
                warnings := make([]warning, len(res.Warnings))
                for i, w := range res.Warnings { warnings[i] = warning{"unsupported", w} }
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                if err := enc.Encode(map[string]any{"files": written, "warnings": warnings}); err != nil { return err }
            }
            return strict.err(nil)
        },
    }
    c.Flags().StringVar(&out, "out", "", "Directory to write one YAML file per schema into (default: print to stdout)")
    c.Flags().StringArrayVar(&schemas, "schema", nil, "Only pull these schemas (repeatable or comma-separated; default: all)")
    c.Flags().StringArrayVar(&excluded, "exclude-schema", nil, "Leave these schemas out (repeatable or comma-separated)")
    c.Flags().BoolVar(&settings, "database-settings", false, "Also write the database's settings when --schema or --exclude-schema limits the pull")
    c.Flags().BoolVar(&owners, "owners", false, "Emit owner: for objects not owned by the connecting role, and for every object of the kinds the config's policy.require_owners lists")
    c.Flags().BoolVar(&roles, "roles", false, "Emit the cluster's roles")
    c.Flags().BoolVar(&force, "force", false, "Overwrite existing files in --out")
    c.Flags().StringArrayVar(&dataTables, "data", nil, "Also pull the rows of these tables (schema.table, repeatable or comma-separated; quote mixed-case names as \"Schema\".\"Table\" or write them as is) as data: blocks")
    return c
}

// nameList splits flag values on commas outside double quotes and removes
// the quotes of "Quoted"."Name" parts, so a mixed-case name can be given
// either quoted or as is.
func nameList(values []string) []string {
    out := []string{}
    for _, v := range values {
        var part strings.Builder
        quoted := false
        flush := func() {
            if s := strings.TrimSpace(part.String()); s != "" { out = append(out, s) }
            part.Reset()
        }
        for i := 0; i < len(v); i++ {
            switch c := v[i]; {
            case c == '"' && quoted && i+1 < len(v) && v[i+1] == '"':
                part.WriteByte('"')
                i++
            case c == '"':
                quoted = !quoted
            case c == ',' && !quoted:
                flush()
            default:
                part.WriteByte(c)
            }
        }
        flush()
    }
    return out
}

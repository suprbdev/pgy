package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/group"
    "github.com/suprbdev/pgy/internal/schema"
    "github.com/suprbdev/pgy/internal/validate"
)

func cmdDiff() *cobra.Command {
    var unsafe bool
    var prune bool
    var jsonOut bool
    var fromEmpty bool
    var split string
    var noWarnings bool
    var strictExpressions bool
    c := &cobra.Command{
        Use:   "diff",
        Short: "Generate SQL diff between YAML schema and live DB",
        Long: `Generate SQL diff between YAML schema and live DB.

Statements that lock or rewrite an existing table (an index built without
concurrently, a foreign key or check added without notValid, SET NOT NULL, a
rewriting type change, SET LOGGED/UNLOGGED, an added column whose value is
computed per row) are reported on stderr, as are the errors pgy validate would
report. Use --no-warnings to silence them.

Declared expressions (defaults, checks, index expressions and predicates,
policies, triggers and the like) are compared as text, and through the server
where the text differs or matches only because a cast or a schema was left
out on one side. --strict-expressions, or policy.strict_expressions in the
config, compares every one through the server.

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
            if cfg.Strict && cfg.ForbidUnsafe && (unsafe || prune) {
                return fmt.Errorf("--strict refuses --unsafe and --prune (set ci.forbid_unsafe: false in the config to allow them)")
            }
            strict := &strictFindings{}
            opts := diff.Options{Unsafe: unsafe, Prune: prune, MigrationsTable: migSchema + "." + migTable, PublicGrants: cfg.Migrate.PublicGrants, KeepExtensions: cfg.KeepExtensions, External: cfg.External}
            schemaPaths, err := resolveSchemaPaths(cfg.SchemaRoot, cfg.Schemas)
            if err != nil {
                return err
            }
            issues := make(chan []validate.Issue, 1)
            if noWarnings && !cfg.failsOn("validate") {
                issues <- nil
            } else {
                go func() { issues <- validate.FilesWith(schemaPaths, cfg.External) }()
            }
            type loaded struct {
                db  *schema.Database
                err error
            }
            load := make(chan loaded, 1)
            go func() {
                d, err := schema.LoadAndMerge(schemaPaths)
                load <- loaded{d, err}
            }()
            var conn interface{ Close() }
            var pool *pgxpool.Pool
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
                    Offline: true,
                }
            } else {
                p, err2 := db.Connect(ctx, cfg.DSN)
                if err2 != nil {
                    return err2
                }
                pool = p
                conn = pool
                defer conn.Close()
                live, err = diff.Introspect(ctx, pool)
                if err != nil {
                    return err
                }
            }

            for _, is := range <-issues {
                if is.Severity == validate.Error && !noWarnings { fmt.Fprintln(os.Stderr, "warning:", is.String()) }
                if cfg.failsOn("validate") { strict.add("validate", is.String()) }
            }
            l := <-load
            if l.err != nil {
                return l.err
            }
            compiled := l.db
            compiled.External = cfg.External
            if !cfg.Policy.Empty() {
                for _, is := range validate.CheckPolicy(compiled, cfg.Policy) {
                    if !noWarnings { fmt.Fprintln(os.Stderr, "warning:", is.String()) }
                    if cfg.failsOn("policy") { strict.add("policy", is.String()) }
                }
            }
            // fail loudly on dependsOn cycles: the plan's entity order would
            // not be dependency-correct
            if _, err := schema.TopologicalSort(compiled); err != nil {
                return err
            }
            if pool != nil {
                if err := diff.IntrospectExtensions(ctx, pool, live, compiled, opts); err != nil {
                    return err
                }
                if err := diff.IntrospectDataWith(ctx, pool, live, compiled, opts); err != nil {
                    return err
                }
                if err := diff.ProbeViews(ctx, pool, live, compiled); err != nil && !noWarnings {
                    fmt.Fprintf(os.Stderr, "warning: views and materialized views were not compared with the YAML, so their queries are left as they are: %v\n", err)
                }
                probe := diff.ProbeExpressions
                if strictExpressions || !cmd.Flags().Changed("strict-expressions") && cfg.Migrate.StrictExpressions { probe = diff.ProbeEveryExpression }
                if err := probe(ctx, pool, live, compiled); err != nil && !noWarnings {
                    fmt.Fprintf(os.Stderr, "warning: expressions were compared as text only, so one PostgreSQL stores the same way as the YAML may be reported as changed, and one that differs only in a cast or a schema as unchanged: %v\n", err)
                }
            }
            var steps []group.Step
            var plan *diff.PlanDiff
            makePlan := func() error {
                if split == "" {
                    planner := diff.PlanReportingUnsafe
                    if fromEmpty { planner = diff.PlanWith }
                    plan = planner(live, compiled, opts)
                    return nil
                }
                g, err := group.Split(schemaPaths, split)
                if err != nil {
                    return err
                }
                if steps, err = group.Plan(live, schemaPaths, g, opts); err != nil {
                    return err
                }
                if extra := diff.UnsafeOnly(liveOrNil(live, fromEmpty), compiled, opts); len(extra) > 0 {
                    if len(steps) == 0 { steps = append(steps, group.Step{Plan: &diff.PlanDiff{}}) }
                    last := steps[len(steps)-1].Plan
                    last.Warnings = append(last.Warnings, extra...)
                }
                return nil
            }
            if err := makePlan(); err != nil {
                return err
            }
            var sql string
            var summary any
            groups := 0
            if split != "" {
                sql = group.Render(steps)
                summary = splitSummary(steps)
                for _, s := range steps {
                    if s.Name != "" || len(s.Plan.Creates)+len(s.Plan.Alters)+len(s.Plan.Drops)+len(s.Plan.Concurrent)+len(s.Plan.Prelude)+len(s.Plan.ExtensionUpdates) > 0 { groups++ }
                    if noWarnings { continue }
                    for _, w := range capUnsafe(s.Plan.Warnings) {
                        if s.Name == "" { fmt.Fprintln(os.Stderr, "warning:", w.Message) } else { fmt.Fprintf(os.Stderr, "warning: [%s] %s\n", s.Name, w.Message) }
                    }
                }
                for _, s := range steps {
                    for _, w := range s.Plan.Warnings {
                        if cfg.failsOn(w.Kind) { strict.add(w.Kind, w.Message) }
                    }
                    cfg.indexPolicy(s.Plan.Warnings, strict, noWarnings)
                }
            } else {
                sql = diff.Render(plan)
                if len(plan.Concurrent)+len(plan.Prelude)+len(plan.ExtensionUpdates) > 0 {
                    sql = group.Render([]group.Step{{Plan: plan}})
                }
                s := map[string]any{}
                for k, v := range plan.Summary() { s[k] = v }
                if len(plan.Warnings) > 0 { s["warnings"] = plan.Warnings }
                summary = s
                if !noWarnings {
                    for _, w := range capUnsafe(plan.Warnings) { fmt.Fprintln(os.Stderr, "warning:", w.Message) }
                }
                for _, w := range plan.Warnings {
                    if cfg.failsOn(w.Kind) { strict.add(w.Kind, w.Message) }
                }
                cfg.indexPolicy(plan.Warnings, strict, noWarnings)
            }

            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                _ = enc.Encode(summary)
            }

            if toStdout {
                if len(sql) == 0 {
                    if !cfg.Quiet { fmt.Fprintln(os.Stderr, "no changes detected") }
                    return strict.err(nil)
                }
                if _, err := os.Stdout.WriteString(sql); err != nil { return err }
                return strict.err(&exitCodeError{code: 2})
            }
            if len(sql) == 0 {
                if err := os.Remove(cfg.BufferPath); err != nil && !os.IsNotExist(err) {
                    return err
                }
                if !cfg.Quiet {
                    fmt.Println("no changes detected")
                }
                return strict.err(nil)
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
            return strict.err(&exitCodeError{code: 2})
        },
    }
    c.Flags().BoolVar(&unsafe, "unsafe", false, "Allow destructive changes (drops/alters)")
    c.Flags().BoolVar(&prune, "prune", false, "With --unsafe: drop tables, views, functions, types, sequences, indexes, constraints and triggers that the YAML no longer declares, in every schema it declares objects in, and the extensions there that a present extensions block does not list (except those in the config's prune.keep_extensions). Run it with ALL schema files: objects declared only in files left out are dropped")
    c.Flags().BoolVar(&jsonOut, "json", false, "Output machine-readable diff summary")
    c.Flags().BoolVar(&noWarnings, "no-warnings", false, "Do not report schema problems or statements that lock or rewrite existing tables")
    c.Flags().BoolVar(&fromEmpty, "from-empty", false, "Generate diff from an empty database (no DSN required)")
    c.Flags().BoolVar(&strictExpressions, "strict-expressions", false, "Compare every declared expression (defaults, checks, index expressions and predicates, policies, triggers, domain and argument defaults) through the server, also those that match as text; catches what the text comparison hides beyond casts and schemas, such as other parentheses. Costs a temporary copy of every table that declares one: about 2 s per diff for 1000 tables, against 0.5 s without it (default: the config's policy.strict_expressions)")
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



func liveOrNil(live *diff.Live, fromEmpty bool) *diff.Live {
    if fromEmpty { return nil }
    return live
}

// maxUnsafeWarnings is how many "needs --unsafe" warnings pgy diff prints
// before summarising the rest.
const maxUnsafeWarnings = 20

// capUnsafe keeps the first maxUnsafeWarnings warnings of kind unsafe in ws
// and replaces the rest with one that counts them. --json and --strict see
// them all.
func capUnsafe(ws []diff.Warning) []diff.Warning {
    out, n := []diff.Warning{}, 0
    for _, w := range ws {
        if w.Kind == "unsafe" {
            if n++; n > maxUnsafeWarnings { continue }
        }
        out = append(out, w)
    }
    if n > maxUnsafeWarnings {
        out = append(out, diff.Warning{Kind: "unsafe", Message: fmt.Sprintf("and %d more statements that need --unsafe; pgy diff --unsafe shows them", n-maxUnsafeWarnings)})
    }
    return out
}

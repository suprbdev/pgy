package cli

import (
    "bufio"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/db"
)

func cmdMigrate() *cobra.Command {
    var until string
    var limit int
    var dryRun bool
    var lockTimeout string
    var statementTimeout string
    c := &cobra.Command{
        Use:   "migrate",
        Short: "Apply pending migrations",
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

            files, err := readMigrationFiles(cfg.MigrationsDir)
            if err != nil { return err }
            if until != "" { files = filterUntil(files, until) }
            if limit > 0 && len(files) > limit { files = files[:limit] }

            applied, err := loadApplied(ctx, pool, schemaName, tableName)
            if err != nil { return err }

            toApply := make([]string, 0)
            for _, f := range files {
                base := filepath.Base(f)
                if _, ok := applied[base]; !ok {
                    toApply = append(toApply, f)
                }
            }

            if dryRun {
                for _, f := range toApply { fmt.Println(f) }
                if len(toApply) == 0 && !cfg.Quiet { fmt.Println("no pending migrations") }
                if len(toApply) > 0 { return &exitCodeError{code: 2} }
                return nil
            }

            _, err = db.WithAdvisoryLock(ctx, pool, func(ctx context.Context) error {
                if lockTimeout != "" { _, _ = pool.Exec(ctx, "set lock_timeout = "+lockTimeout) }
                if statementTimeout != "" { _, _ = pool.Exec(ctx, "set statement_timeout = "+statementTimeout) }
                for _, f := range toApply {
                    b, err := os.ReadFile(f)
                    if err != nil { return err }
                    sum := checksumBody(b)
                    // verify checksum header if present
                    if hx := parseChecksumHeader(string(b)); hx != "" && hx != sum {
                        return fmt.Errorf("checksum mismatch for %s", f)
                    }
                    if err := applyOne(ctx, pool, string(b)); err != nil { return fmt.Errorf("%s: %w", f, err) }
                    base := filepath.Base(f)
                    if err := recordApplied(ctx, pool, schemaName, tableName, base, sum); err != nil { return err }
                    if !cfg.Quiet { fmt.Printf("applied %s\n", base) }
                }
                return nil
            })
            if err != nil { return err }
            if len(toApply) > 0 { return &exitCodeError{code: 2} }
            return nil
        },
    }
    c.Flags().StringVar(&until, "until", "", "Apply up to a specific migration (prefix ok)")
    c.Flags().IntVar(&limit, "limit", 0, "Apply only N pending migrations")
    c.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be applied")
    c.Flags().StringVar(&lockTimeout, "lock-timeout", "", "SET lock_timeout (e.g. 5s)")
    c.Flags().StringVar(&statementTimeout, "statement-timeout", "", "SET statement_timeout (e.g. 30s)")
    return c
}

func readMigrationFiles(dir string) ([]string, error) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        if os.IsNotExist(err) { return nil, nil }
        return nil, err
    }
    names := []string{}
    for _, e := range entries {
        if e.IsDir() { continue }
        if strings.HasSuffix(e.Name(), ".sql") { names = append(names, filepath.Join(dir, e.Name())) }
    }
    sort.Strings(names)
    return names, nil
}

func filterUntil(files []string, until string) []string {
    out := []string{}
    for _, f := range files {
        base := filepath.Base(f)
        out = append(out, f)
        if strings.HasPrefix(base, until) { break }
    }
    return out
}

func loadApplied(ctx context.Context, pool *db.PoolShim, schema, table string) (map[string]string, error) { return db.LoadApplied(ctx, pool, schema, table) }

func applyOne(ctx context.Context, pool *db.PoolShim, sql string) error { return db.ApplyInTx(ctx, pool, sql) }

func recordApplied(ctx context.Context, pool *db.PoolShim, schema, table, name, checksum string) error { return db.RecordApplied(ctx, pool, schema, table, name, checksum, time.Now().UTC()) }

func checksumBody(b []byte) string {
    // ignore header comment lines starting with -- up to first blank line
    s := string(b)
    lines := strings.Split(s, "\n")
    body := []string{}
    headerDone := false
    for _, ln := range lines {
        if !headerDone && strings.HasPrefix(strings.TrimSpace(ln), "--") {
            continue
        }
        if !headerDone && strings.TrimSpace(ln) == "" {
            headerDone = true
            continue
        }
        headerDone = true
        body = append(body, ln)
    }
    sum := sha256.Sum256([]byte(strings.Join(body, "\n")))
    return hex.EncodeToString(sum[:])
}

func parseChecksumHeader(s string) string {
    sc := bufio.NewScanner(strings.NewReader(s))
    for sc.Scan() {
        ln := strings.TrimSpace(sc.Text())
        if !strings.HasPrefix(ln, "--") { break }
        ln = strings.TrimPrefix(ln, "--")
        ln = strings.TrimSpace(ln)
        if strings.HasPrefix(ln, "checksum ") {
            return strings.TrimSpace(strings.TrimPrefix(ln, "checksum "))
        }
    }
    return ""
}



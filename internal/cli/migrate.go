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
        Long: `Apply pending migrations to the database.

Exit codes:
  0  Success (migrations applied, or already up to date)
  2  --dry-run only: pending migrations exist
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

            files, err := readMigrationFiles(cfg.MigrationsDir)
            if err != nil { return err }
            if until != "" {
                if files, err = filterUntil(files, until); err != nil { return err }
            }
            pending := func() ([]string, error) {
                applied, err := db.LoadApplied(ctx, pool, schemaName, tableName)
                if err != nil { return nil, err }
                out := []string{}
                for _, f := range files {
                    if _, ok := applied[filepath.Base(f)]; !ok { out = append(out, f) }
                }
                if limit > 0 && len(out) > limit { out = out[:limit] }
                return out, nil
            }

            if dryRun {
                toApply, err := pending()
                if err != nil { return err }
                for _, f := range toApply { fmt.Println(f) }
                if len(toApply) == 0 && !cfg.Quiet { fmt.Println("no pending migrations") }
                if len(toApply) > 0 { return &exitCodeError{code: 2} }
                return nil
            }

            return db.WithAdvisoryLock(ctx, pool, func(ctx context.Context) error {
                toApply, err := pending()
                if err != nil { return err }
                if len(toApply) == 0 && !cfg.Quiet { fmt.Println("no pending migrations") }
                for _, f := range toApply {
                    b, err := os.ReadFile(f)
                    if err != nil { return err }
                    sum := checksumBody(b)
                    if hx := parseChecksumHeader(string(b)); hx != "" && hx != sum {
                        return fmt.Errorf("checksum mismatch for %s", f)
                    }
                    base := filepath.Base(f)
                    err = db.ApplyMigration(ctx, pool, db.Migration{
                        SQL: string(b), Name: base, Checksum: sum,
                        LockTimeout: lockTimeout, StatementTimeout: statementTimeout,
                        RecordIn: schemaName + "." + tableName,
                    })
                    if err != nil { return fmt.Errorf("%s: %w", f, err) }
                    if !cfg.Quiet { fmt.Printf("applied %s\n", base) }
                }
                return nil
            })
        },
    }
    c.Flags().StringVar(&until, "until", "", "Apply up to a specific migration (prefix ok)")
    c.Flags().IntVar(&limit, "limit", 0, "Apply at most N pending migrations")
    c.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be applied")
    c.Flags().StringVar(&lockTimeout, "lock-timeout", "", "SET lock_timeout (e.g. 5s)")
    c.Flags().StringVar(&statementTimeout, "statement-timeout", "", "SET statement_timeout (e.g. 30s)")
    return c
}

// readMigrationFiles returns the .sql files in dir in name order. Two files
// sharing a version number (e.g. 0005_a.sql and 0005_b.sql from parallel
// branches) are an error: the migrations table is keyed by version, so only
// one of them could ever be recorded.
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
    seen := map[int]string{}
    for _, n := range names {
        base := filepath.Base(n)
        v := db.MigrationVersion(base)
        if prev, ok := seen[v]; ok {
            return nil, fmt.Errorf("migrations %s and %s share version %d; renumber one of them", prev, base, v)
        }
        seen[v] = base
    }
    return names, nil
}

// filterUntil returns files up to and including the first one whose name
// starts with until (a version such as 0003, or a full name with or without
// .sql). It is an error if no file matches.
func filterUntil(files []string, until string) ([]string, error) {
    until = strings.TrimSuffix(filepath.Base(until), ".sql")
    for i, f := range files {
        if strings.HasPrefix(filepath.Base(f), until) { return files[:i+1], nil }
    }
    return nil, fmt.Errorf("no migration matches %q", until)
}

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



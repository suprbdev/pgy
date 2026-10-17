package cli

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestSlugify(t *testing.T) {
    cases := map[string]string{
        "Users & Auth": "users_auth",
        "  Hello-World  ": "hello_world",
        "": "",
    }
    for in, want := range cases {
        if got := slugify(in); got != want {
            t.Fatalf("slugify(%q)=%q want %q", in, got, want)
        }
    }
}

func TestNextMigrationNumber(t *testing.T) {
    dir := t.TempDir()
    // empty -> 1
    n, err := nextMigrationNumber(dir)
    if err != nil || n != 1 { t.Fatalf("got %d %v", n, err) }
    // create some files
    for _, name := range []string{"0001_init.sql", "0002_more.sql"} {
        if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil { t.Fatal(err) }
    }
    n, err = nextMigrationNumber(dir)
    if err != nil || n != 3 { t.Fatalf("got %d %v", n, err) }
}

func runCommit(t *testing.T, buffer string, args ...string) (string, error) {
    t.Helper()
    dir := t.TempDir()
    cfg := &GlobalConfig{BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: filepath.Join(dir, "migrations"), Quiet: true}
    if err := os.MkdirAll(cfg.MigrationsDir, 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(cfg.MigrationsDir, "0001_existing.sql"), []byte("select 1;\n"), 0o644); err != nil { t.Fatal(err) }
    if err := os.WriteFile(cfg.BufferPath, []byte(buffer), 0o644); err != nil { t.Fatal(err) }
    c := cmdCommit()
    c.SetArgs(args)
    err := c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
    return cfg.MigrationsDir, err
}

func TestCommitGroupedBuffer(t *testing.T) {
    buffer := "-- pgy:group modules/core/user\ncreate table a (id int);\n\n-- pgy:group links/empty\n\n-- pgy:group links/a_b\nalter table a add column b int;\n"
    dir, err := runCommit(t, buffer, "init")
    if err != nil { t.Fatal(err) }
    files, err := readMigrationFiles(dir)
    if err != nil { t.Fatal(err) }
    var names []string
    for _, f := range files { names = append(names, filepath.Base(f)) }
    want := []string{"0001_existing.sql", "0002_init_modules_core_user.sql", "0003_init_links_a_b.sql"}
    if strings.Join(names, ",") != strings.Join(want, ",") { t.Fatalf("got %v want %v", names, want) }
    for _, f := range files[1:] {
        b, err := os.ReadFile(f)
        if err != nil { t.Fatal(err) }
        if hx := parseChecksumHeader(string(b)); hx == "" || hx != checksumBody(b) { t.Fatalf("checksum mismatch in %s", f) }
        if strings.Contains(string(b), "pgy:group") { t.Fatalf("group marker leaked into %s", f) }
    }
    b, _ := os.ReadFile(files[2])
    if !strings.HasSuffix(string(b), "\n\nalter table a add column b int;\n") { t.Fatalf("unexpected body: %q", b) }
}

func TestCommitGroupedBufferWithoutName(t *testing.T) {
    dir, err := runCommit(t, "-- pgy:group users\nselect 1;\n")
    if err != nil { t.Fatal(err) }
    if _, err := os.Stat(filepath.Join(dir, "0002_users.sql")); err != nil { t.Fatal(err) }
}

func TestCommitGroupedBufferAllEmpty(t *testing.T) {
    if _, err := runCommit(t, "-- pgy:group a\n\n-- pgy:group b\n"); err == nil || !strings.Contains(err.Error(), "empty") {
        t.Fatalf("expected empty buffer error, got %v", err)
    }
}

func TestCommitPlainBuffer(t *testing.T) {
    dir, err := runCommit(t, "create table a (id int);\n", "Users & Auth")
    if err != nil { t.Fatal(err) }
    b, err := os.ReadFile(filepath.Join(dir, "0002_users_auth.sql"))
    if err != nil { t.Fatal(err) }
    if parseChecksumHeader(string(b)) != checksumBody(b) { t.Fatal("checksum mismatch") }
}

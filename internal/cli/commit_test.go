package cli

import (
    "os"
    "path/filepath"
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



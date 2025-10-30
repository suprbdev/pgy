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
    os.WriteFile(filepath.Join(dir, "0001_init.sql"), []byte(""), 0o644)
    os.WriteFile(filepath.Join(dir, "0002_more.sql"), []byte(""), 0o644)
    n, err = nextMigrationNumber(dir)
    if err != nil || n != 3 { t.Fatalf("got %d %v", n, err) }
}



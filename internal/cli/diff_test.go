package cli

import (
    "path/filepath"
    "strings"
    "testing"
)

func TestResolveSchemaPaths(t *testing.T) {
    root := t.TempDir()
    other := t.TempDir()
    writeFile(t, filepath.Join(root, "a.yml"), "tables: {}\n")
    abs := filepath.Join(other, "b.yml")
    writeFile(t, abs, "tables: {}\n")

    got, err := resolveSchemaPaths(root, []string{"a.yml", abs})
    if err != nil { t.Fatal(err) }
    if len(got) != 2 || got[0] != filepath.Join(root, "a.yml") || got[1] != abs {
        t.Fatalf("got %v", got)
    }

    _, err = resolveSchemaPaths(root, []string{"a.yml", "typo.yml"})
    if err == nil || !strings.Contains(err.Error(), "schema file not found") || !strings.Contains(err.Error(), "typo.yml") {
        t.Fatalf("expected missing file error, got %v", err)
    }
}

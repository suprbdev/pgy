package cli

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestHistoryWarnings(t *testing.T) {
    dir := t.TempDir()
    write := func(name, body string) string {
        p := filepath.Join(dir, name)
        if err := os.WriteFile(p, []byte(body), 0o644); err != nil { t.Fatal(err) }
        return p
    }
    files := []string{
        write("0001_a.sql", "create table a (id int);\n"),
        write("0002_b.sql", "create table b (id int);\n"),
        write("0003_late.sql", "create table c (id int);\n"),
        write("0004_d.sql", "create table d (id int);\n"),
    }
    applied := map[string]string{
        "0001_a.sql":    checksumBody([]byte("create table a (id int);\n")),
        "0002_b.sql":    checksumBody([]byte("create table b (id bigint);\n")),
        "0004_d.sql":    checksumBody([]byte("create table d (id int);\n")),
        "0005_gone.sql": "abc",
    }
    got := strings.Join(historyWarnings(files, applied), "\n")
    for _, want := range []string{
        "0002_b.sql changed after it was applied",
        "0003_late.sql is pending but 0005_gone.sql is already applied; it will run out of order",
        "0005_gone.sql is recorded as applied but has no file",
    } {
        if !strings.Contains(got, want) { t.Errorf("missing %q in:\n%s", want, got) }
    }
    if strings.Contains(got, "0001_a.sql") || strings.Contains(got, "0004_d.sql") {
        t.Errorf("unchanged applied migrations must not warn:\n%s", got)
    }
}

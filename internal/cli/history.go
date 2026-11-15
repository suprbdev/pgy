package cli

import (
    "fmt"
    "os"
    "path/filepath"
    "sort"

    "github.com/suprbdev/pgy/internal/db"
)

// historyWarnings compares the migration files with the migrations recorded
// as applied and describes what looks wrong: applied files whose content
// changed since they ran, applied migrations whose file is gone, and pending
// files numbered below the latest applied migration (they will run out of
// order, typically after merging branches).
func historyWarnings(files []string, applied map[string]string) []string {
    out := []string{}
    present := map[string]bool{}
    latest, latestName := -1, ""
    for name := range applied {
        if v := db.MigrationVersion(name); v > latest {
            latest, latestName = v, name
        }
    }
    for _, f := range files {
        base := filepath.Base(f)
        present[base] = true
        recorded, ok := applied[base]
        if !ok {
            if v := db.MigrationVersion(base); v < latest {
                out = append(out, fmt.Sprintf("%s is pending but %s is already applied; it will run out of order", base, latestName))
            }
            continue
        }
        b, err := os.ReadFile(f)
        if err != nil || recorded == "" { continue }
        if sum := checksumBody(b); sum != recorded {
            out = append(out, fmt.Sprintf("%s changed after it was applied; the change will not be applied (recorded checksum %.12s, file %.12s)", base, recorded, sum))
        }
    }
    missing := []string{}
    for name := range applied {
        if !present[name] { missing = append(missing, name) }
    }
    sort.Strings(missing)
    for _, name := range missing {
        out = append(out, fmt.Sprintf("%s is recorded as applied but has no file", name))
    }
    return out
}

func printHistoryWarnings(files []string, applied map[string]string) {
    for _, w := range historyWarnings(files, applied) { fmt.Fprintln(os.Stderr, "warning:", w) }
}

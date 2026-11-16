package cli

import (
    "fmt"
    "os"
    "sort"
    "strings"

    "github.com/suprbdev/pgy/internal/diff"
)

// strictFindings collects the findings a --strict run fails on.
type strictFindings struct {
    count int
    kinds map[string]bool
}

func (s *strictFindings) add(kind, _ string) {
    if kind == "" { kind = "other" }
    if s.kinds == nil { s.kinds = map[string]bool{} }
    s.kinds[kind] = true
    s.count++
}

// err returns an error naming the findings when there are any, which makes
// the command exit 1, and otherwise passes ok through.
func (s *strictFindings) err(ok error) error {
    if s.count == 0 { return ok }
    kinds := make([]string, 0, len(s.kinds))
    for k := range s.kinds { kinds = append(kinds, k) }
    sort.Strings(kinds)
    return fmt.Errorf("--strict: %d finding(s) of kind %s, which ci.fail_on fails on", s.count, strings.Join(kinds, ", "))
}

// indexPolicy reports each blocking index build in ws as a policy finding
// when the config sets policy.concurrent_indexes.
func (c *GlobalConfig) indexPolicy(ws []diff.Warning, strict *strictFindings, quiet bool) {
    if !c.Migrate.ConcurrentIndexes { return }
    for _, w := range ws {
        if !w.BlockingIndexBuild() { continue }
        msg := fmt.Sprintf("policy: indexes on existing tables must set concurrently: true (concurrent_indexes): %s", strings.TrimSuffix(w.Statement, ";"))
        if !quiet { fmt.Fprintln(os.Stderr, "warning:", msg) }
        if c.failsOn("policy") { strict.add("policy", msg) }
    }
}

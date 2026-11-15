package cli

import (
    "fmt"
    "sort"
    "strings"
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

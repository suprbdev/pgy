package group

import (
    "strings"

    "github.com/suprbdev/pgy/internal/diff"
)

const marker = "-- pgy:group"

type Section struct {
    Name string
    SQL  string
}

func Render(steps []Step) string {
    var b strings.Builder
    for i, s := range steps {
        if i > 0 { b.WriteString("\n") }
        b.WriteString(marker + " " + s.Name + "\n")
        b.WriteString(diff.Render(s.Plan))
    }
    return b.String()
}

func Parse(sql string) []Section {
    var out []Section
    var lead, body []string
    found := false
    flush := func() {
        if len(out) > 0 { out[len(out)-1].SQL = joinBody(body) }
        body = nil
    }
    for _, ln := range strings.SplitAfter(sql, "\n") {
        if name, ok := markerName(ln); ok {
            if !found {
                lead = body
                body = nil
            }
            flush()
            found = true
            out = append(out, Section{Name: name})
            continue
        }
        body = append(body, ln)
    }
    if !found { return nil }
    flush()
    if s := joinBody(lead); s != "" { out = append([]Section{{SQL: s}}, out...) }
    return out
}

func markerName(line string) (string, bool) {
    s := strings.TrimSpace(line)
    if !strings.HasPrefix(s, marker) { return "", false }
    rest := s[len(marker):]
    if rest != "" && rest[0] != ' ' && rest[0] != '\t' { return "", false }
    return strings.TrimSpace(rest), true
}

func joinBody(lines []string) string {
    s := strings.TrimSpace(strings.Join(lines, ""))
    if s == "" { return "" }
    return s + "\n"
}

package diff

import (
    "strings"
    "unicode/utf8"

    "github.com/suprbdev/pgy/internal/schema"
)

// maxIdentifierLength is PostgreSQL's NAMEDATALEN-1. Longer identifiers are
// silently truncated by the server, so names must be compared in that form.
const maxIdentifierLength = 63

// truncateIdent truncates s to maxIdentifierLength bytes without splitting a
// multibyte character, as PostgreSQL does.
func truncateIdent(s string) string {
    return clipBytes(s, maxIdentifierLength)
}

func clipBytes(s string, n int) string {
    if len(s) <= n { return s }
    for n > 0 && !utf8.RuneStart(s[n]) { n-- }
    return s[:n]
}

// makeObjectName joins base and suffix with an underscore, truncating base
// rather than the suffix when the result would exceed maxIdentifierLength, so
// an index (_idx) and a unique constraint (_key) on the same long column never
// truncate to the same name.
func makeObjectName(base, suffix string) string {
    return clipBytes(base, maxIdentifierLength-len(suffix)-1) + "_" + suffix
}

// splitIndexOrdering splits a trailing ASC/DESC and NULLS FIRST/LAST suffix
// off an index column entry: "created_at desc nulls last" returns
// ("created_at", "desc nulls last").
func splitIndexOrdering(col string) (base, ordering string) {
    words := strings.Fields(col)
    n := len(words)
    suffix := []string{}
    if n >= 3 && strings.EqualFold(words[n-2], "nulls") &&
        (strings.EqualFold(words[n-1], "first") || strings.EqualFold(words[n-1], "last")) {
        suffix = append([]string{"nulls", strings.ToLower(words[n-1])}, suffix...)
        n -= 2
    }
    if n >= 2 && (strings.EqualFold(words[n-1], "asc") || strings.EqualFold(words[n-1], "desc")) {
        suffix = append([]string{strings.ToLower(words[n-1])}, suffix...)
        n--
    }
    if len(suffix) == 0 { return strings.TrimSpace(col), "" }
    return strings.Join(words[:n], " "), strings.Join(suffix, " ")
}

func indexNameBase(fq string, ix *schema.Index) string {
    cols := make([]string, len(ix.Columns))
    for i, c := range ix.Columns { cols[i], _ = splitIndexOrdering(c) }
    return strings.ReplaceAll(fq+"_"+strings.Join(cols, "_"), ".", "_")
}

// indexName returns the index's name as PostgreSQL stores it: the declared
// name truncated to maxIdentifierLength, or the auto-generated
// <schema>_<table>_<cols>_idx name when none was given.
func indexName(fq string, ix *schema.Index) string {
    if ix.Name != "" { return truncateIdent(ix.Name) }
    return makeObjectName(indexNameBase(fq, ix), "idx")
}

// LiveIndexName returns the name under which index ix exists in live, or its
// indexName when it does not exist. Auto-generated names longer than
// maxIdentifierLength used to be emitted untruncated and cut short by
// PostgreSQL; an index created that way is still matched.
func LiveIndexName(fq string, ix *schema.Index, live map[string]bool) string {
    name := indexName(fq, ix)
    if ix.Name == "" && !live[name] {
        if legacy := truncateIdent(indexNameBase(fq, ix) + "_idx"); live[legacy] { return legacy }
    }
    return name
}

func uniqueKeyBase(fq, col string) string {
    return strings.ReplaceAll(fq+"_"+col, ".", "_")
}

// uniqueKeyName returns the generated name of the unique constraint behind a
// column-level `unique: true` flag.
func uniqueKeyName(fq, col string) string {
    return makeObjectName(uniqueKeyBase(fq, col), "key")
}

// LiveUniqueKeyName is LiveIndexName for a column-level unique constraint,
// looked up in live constraint names.
func LiveUniqueKeyName(fq, col string, live map[string]bool) string {
    name := uniqueKeyName(fq, col)
    if !live[name] {
        if legacy := truncateIdent(uniqueKeyBase(fq, col) + "_key"); live[legacy] { return legacy }
    }
    return name
}

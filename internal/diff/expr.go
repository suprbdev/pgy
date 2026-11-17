package diff

import (
    "fmt"
    "regexp"
    "strconv"
    "strings"
)

var (
    exprCastRe = regexp.MustCompile(`(?i)::(?:"[^"]*"|[a-z_][\w$]*)(?:\.(?:"[^"]*"|[a-z_][\w$]*))*` +
        `(?:\s*\(\s*\d+(?:\s*,\s*\d+)?\s*\))?` +
        `(?:\s+(?:varying|precision|with(?:out)?\s+time\s+zone))?` +
        `(?:\s*\(\s*\d+(?:\s*,\s*\d+)?\s*\))?` +
        `(?:\s*\[\d*\])*`)
    exprNotInListRe = regexp.MustCompile(`(?i)\bnot\s+in\s*\(([^()]*)\)`)
    exprInListRe    = regexp.MustCompile(`(?i)\bin\s*\(([^()]*)\)`)
    exprOperand     = `((?:[\w.]+|"[^"]*")(?:\([^()]*\))?|\x00\d+\x00)`
    exprBound       = `(-?(?:[\w.]+|"[^"]*")(?:\([^()]*\))?|\x00\d+\x00)`
    exprNotBetweenRe = regexp.MustCompile(`(?i)` + exprOperand + `\s+not\s+between\s+` + exprBound + `\s+and\s+` + exprBound)
    exprBetweenRe    = regexp.MustCompile(`(?i)` + exprOperand + `\s+between\s+` + exprBound + `\s+and\s+` + exprBound)
    exprCatalogRe    = regexp.MustCompile(`(?i)(^|[^\w$."])"?pg_catalog"?\.`)
    exprLikeRe       = regexp.MustCompile(`(?i)\b(not\s+)?(i?like)\b`)
    exprTypedLiteralRe = regexp.MustCompile(`(?i)\b(?:interval|date|time|timestamp|timestamptz|numeric|decimal|text|jsonb|json|uuid|inet|cidr|bool|boolean|bytea|int|integer|int4|int8|bigint|smallint|real|float8|varchar|char)\s+(\x00\d+\x00)`)
    exprPlaceholderRe  = regexp.MustCompile(`\x00(\d+)\x00`)
    exprNumericRe      = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
)

// canonicalizeExpr rewrites a SQL expression into the shape PostgreSQL's
// deparser (pg_get_expr, pg_get_constraintdef) prints it in, so a YAML
// expression and its live echo can be compared textually:
//
//   - pg_catalog. qualifiers are dropped: pg_catalog is always searched
//     first, so PostgreSQL never prints them for a name it resolves there
//   - the qualifiers of the schemas on the session's search_path (see
//     nameScope) are dropped, and OPERATOR(schema.op) for one of them
//     becomes op: PostgreSQL always prints both, and the YAML may leave
//     them out
//   - ::type casts are removed, including multi-word and modified types
//     ('a'::character varying(10), x::timestamp(3) with time zone), and
//     CAST(x AS type) becomes (x)
//   - x IN (a, b) becomes x = ANY (ARRAY[a, b]); NOT IN becomes <> ALL
//     (a single-element list becomes = / <>); subquery INs are left alone
//   - x BETWEEN a AND b becomes x >= a AND x <= b (NOT BETWEEN: x < a OR x > b);
//     the bounds may be negative
//   - LIKE / ILIKE / NOT LIKE / NOT ILIKE become ~~ / ~~* / !~~ / !~~*
//   - != becomes <>
//   - typed literals (interval '1 day') lose their type prefix, matching the
//     cast-stripped live form ('1 day'::interval)
//   - quoted numeric literals ('-1', as PostgreSQL stores negative constants)
//     are unquoted
//
// Single-quoted string literals are never rewritten.
func canonicalizeExpr(s string) string {
    masked, lits := maskStringLiterals(s)
    masked = activeScope().unqualifySQL(masked)
    masked = exprCatalogRe.ReplaceAllString(masked, "$1")
    masked = rewriteCastCalls(masked)
    masked = exprCastRe.ReplaceAllString(masked, "")
    masked = strings.ReplaceAll(masked, "!=", "<>")
    masked = maskPositionIn(masked)
    masked = exprNotInListRe.ReplaceAllStringFunc(masked, func(m string) string {
        return rewriteInList(exprNotInListRe.FindStringSubmatch(m)[1], "<>", "all", m)
    })
    masked = exprInListRe.ReplaceAllStringFunc(masked, func(m string) string {
        return rewriteInList(exprInListRe.FindStringSubmatch(m)[1], "=", "any", m)
    })
    masked = exprNotBetweenRe.ReplaceAllString(masked, "$1 < $2 or $1 > $3")
    masked = exprBetweenRe.ReplaceAllString(masked, "$1 >= $2 and $1 <= $3")
    masked = exprLikeRe.ReplaceAllStringFunc(masked, func(m string) string {
        sm := exprLikeRe.FindStringSubmatch(m)
        op := "~~"
        if strings.EqualFold(sm[2], "ilike") { op += "*" }
        if sm[1] != "" { op = "!" + op }
        return op
    })
    masked = exprTypedLiteralRe.ReplaceAllString(masked, "$1")
    masked = strings.ReplaceAll(masked, "\x01", "in")
    return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
        i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
        lit := lits[i]
        if inner := lit[1 : len(lit)-1]; exprNumericRe.MatchString(inner) {
            return inner
        }
        return lit
    })
}

// rewriteCastCalls replaces each CAST(x AS type) in s, whose string
// literals are already masked, with (x), which is what x::type becomes once
// casts are removed. Nested calls are rewritten from the outside in.
func rewriteCastCalls(s string) string {
    for start := 0; ; {
        i := strings.Index(strings.ToLower(s[start:]), "cast")
        if i < 0 { return s }
        i += start
        j := i + len("cast")
        for j < len(s) && s[j] == ' ' { j++ }
        start = i + len("cast")
        if (i > 0 && (isWordByte(s[i-1]) || s[i-1] == '"' || s[i-1] == '.')) || j >= len(s) || s[j] != '(' { continue }
        depth, as, end := 0, -1, -1
        for k := j; k < len(s) && end < 0; k++ {
            switch s[k] {
            case '(':
                depth++
            case ')':
                if depth--; depth == 0 { end = k }
            case ' ', '\t', '\n':
                if depth == 1 && k+4 <= len(s) && strings.EqualFold(s[k+1:k+3], "as") && strings.ContainsRune(" \t\n", rune(s[k+3])) { as = k }
            }
        }
        if end < 0 || as < 0 { continue }
        s = s[:i] + "(" + strings.TrimSpace(s[j+1:as]) + ")" + s[end+1:]
        start = i
    }
}

// maskPositionIn replaces the IN keyword of position(a IN b) calls with \x01
// so it is not mistaken for an IN list (PostgreSQL deparses the call as
// POSITION((a) IN (b))).
func maskPositionIn(s string) string {
    lower := strings.ToLower(s)
    out := []byte(s)
    for start := 0; ; {
        i := strings.Index(lower[start:], "position")
        if i < 0 { break }
        i += start
        j := i + len("position")
        for j < len(s) && s[j] == ' ' { j++ }
        start = j
        if (i > 0 && isWordByte(s[i-1])) || j >= len(s) || s[j] != '(' { continue }
        depth := 0
        for k := j; k < len(s); k++ {
            switch s[k] {
            case '(':
                depth++
            case ')':
                depth--
            }
            if depth == 0 { break }
            if depth == 1 && k+2 <= len(s) && strings.EqualFold(s[k:k+2], "in") && !isWordByte(s[k-1]) && (k+2 == len(s) || !isWordByte(s[k+2])) {
                out[k], out[k+1] = 0x01, ' '
            }
        }
    }
    return string(out)
}

func isWordByte(c byte) bool {
    return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func rewriteInList(list, op, quantifier, orig string) string {
    if strings.Contains(strings.ToLower(list), "select") || strings.TrimSpace(list) == "" {
        return orig
    }
    if !strings.Contains(list, ",") {
        return op + " " + strings.TrimSpace(list)
    }
    return fmt.Sprintf("%s %s (array[%s])", op, quantifier, list)
}

// maskStringLiterals replaces each single-quoted literal (with '' escapes)
// by a \x00<n>\x00 placeholder and returns the literals in order.
func maskStringLiterals(s string) (string, []string) {
    var b strings.Builder
    lits := []string{}
    for i := 0; i < len(s); i++ {
        if s[i] != '\'' {
            b.WriteByte(s[i])
            continue
        }
        j := i + 1
        for j < len(s) {
            if s[j] == '\'' {
                if j+1 < len(s) && s[j+1] == '\'' {
                    j += 2
                    continue
                }
                break
            }
            j++
        }
        if j >= len(s) {
            b.WriteString(s[i:])
            break
        }
        fmt.Fprintf(&b, "\x00%d\x00", len(lits))
        lits = append(lits, s[i:j+1])
        i = j
    }
    return b.String(), lits
}

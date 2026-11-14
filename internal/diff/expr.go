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
    exprNotBetweenRe = regexp.MustCompile(`(?i)` + exprOperand + `\s+not\s+between\s+` + exprOperand + `\s+and\s+` + exprOperand)
    exprBetweenRe    = regexp.MustCompile(`(?i)` + exprOperand + `\s+between\s+` + exprOperand + `\s+and\s+` + exprOperand)
    exprLikeRe       = regexp.MustCompile(`(?i)\b(not\s+)?(i?like)\b`)
    exprTypedLiteralRe = regexp.MustCompile(`(?i)\b(?:interval|date|time|timestamp|timestamptz|numeric|decimal|text|jsonb|json|uuid|inet|cidr|bool|boolean|bytea|int|integer|int4|int8|bigint|smallint|real|float8|varchar|char)\s+(\x00\d+\x00)`)
    exprPlaceholderRe  = regexp.MustCompile(`\x00(\d+)\x00`)
    exprNumericRe      = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
)

// canonicalizeExpr rewrites a SQL expression into the shape PostgreSQL's
// deparser (pg_get_expr, pg_get_constraintdef) prints it in, so a YAML
// expression and its live echo can be compared textually:
//
//   - ::type casts are removed, including multi-word and modified types
//     ('a'::character varying(10), x::timestamp(3) with time zone)
//   - x IN (a, b) becomes x = ANY (ARRAY[a, b]); NOT IN becomes <> ALL
//     (a single-element list becomes = / <>); subquery INs are left alone
//   - x BETWEEN a AND b becomes x >= a AND x <= b (NOT BETWEEN: x < a OR x > b)
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
    masked = exprCastRe.ReplaceAllString(masked, "")
    masked = strings.ReplaceAll(masked, "!=", "<>")
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
    return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
        i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
        lit := lits[i]
        if inner := lit[1 : len(lit)-1]; exprNumericRe.MatchString(inner) {
            return inner
        }
        return lit
    })
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

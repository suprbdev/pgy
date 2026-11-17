package diff

import (
    "fmt"
    "regexp"
    "strconv"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

// Warning flags a planned statement that takes a strong lock on, or
// rewrites, a table that already exists. Table is "schema.table".
type Warning struct {
    Table     string `json:"table"`
    Statement string `json:"statement,omitempty"`
    Message   string `json:"message"`
    // Kind classifies the warning for --strict: lock (a statement locks or
    // rewrites a table), public_revoke (a grants block revokes PUBLIC's
    // privileges), dependents (a type change is blocked by objects pgy
    // can't recreate), or "" for the rest.
    Kind      string `json:"kind,omitempty"`
}

const quotedName = `"(?:[^"]|"")*"`

var (
    lockFQ          = `(` + quotedName + `(?:\.` + quotedName + `)?)`
    lockIndexRe     = regexp.MustCompile(`^create( unique)? index if not exists (` + quotedName + `) on ` + lockFQ)
    lockConstraintRe = regexp.MustCompile(`^alter table ` + lockFQ + ` add constraint (` + quotedName + `) (foreign key|check|unique|exclude)`)
    lockPrimaryKeyRe = regexp.MustCompile(`^alter table ` + lockFQ + ` add primary key`)
    lockNotNullRe   = regexp.MustCompile(`^alter table ` + lockFQ + ` alter column (` + quotedName + `) set not null;$`)
    lockTypeRe      = regexp.MustCompile(`^alter table ` + lockFQ + ` alter column (` + quotedName + `) type (.+);$`)
    lockLoggedRe    = regexp.MustCompile(`^alter table ` + lockFQ + ` set (logged|unlogged);$`)
    lockAddColumnRe = regexp.MustCompile(`^alter table ` + lockFQ + ` add column (` + quotedName + `) (.+);$`)
    volatileCallRe  = regexp.MustCompile(`(?i)\b(random|gen_random_uuid|uuid_generate_v[14]|uuidv[47]|clock_timestamp|timeofday|nextval)\s*\(`)
    typeModsRe      = regexp.MustCompile(`^([a-z ]+?)(?:\((\d+)(?:,(\d+))?\))?$`)
)

// unquoteFQ turns a pqIdent-quoted "schema"."name" back into schema.name.
func unquoteFQ(s string) string {
    parts := []string{}
    for len(s) > 0 && s[0] == '"' {
        i := 1
        for i < len(s) {
            if s[i] == '"' {
                if i+1 < len(s) && s[i+1] == '"' { i += 2; continue }
                break
            }
            i++
        }
        parts = append(parts, strings.ReplaceAll(s[1:i], `""`, `"`))
        s = strings.TrimPrefix(s[min(i+1, len(s)):], ".")
    }
    return strings.Join(parts, ".")
}

// lockWarnings returns a warning for each statement in p that locks or
// rewrites a table present in live (the snapshot the plan was made
// against): index builds without CONCURRENTLY, constraints validated while
// they are added, SET NOT NULL, rewriting type changes, SET LOGGED /
// UNLOGGED, and added columns whose values must be computed per row.
func lockWarnings(live *Live, p *PlanDiff) []Warning {
    if live == nil { return nil }
    exists := func(fq string) bool {
        if lt := live.Tables[fq]; lt != nil { return lt.Foreign == nil }
        return live.MatViewTables[fq] != nil
    }
    out := []Warning{}
    warn := func(fq, stmt, format string, args ...any) {
        out = append(out, Warning{Table: fq, Statement: stmt, Message: fmt.Sprintf(format, args...), Kind: "lock"})
    }
    for _, stmt := range append(append([]string{}, p.Creates...), p.Alters...) {
        if m := lockIndexRe.FindStringSubmatch(stmt); m != nil {
            if fq := unquoteFQ(m[3]); exists(fq) {
                if lt := live.Tables[fq]; lt != nil && lt.Partitioned {
                    warn(fq, stmt, "CREATE INDEX %s blocks writes to every partition of %s while it builds; set concurrently: true to build it on each partition concurrently and attach the partition indexes", unquoteFQ(m[2]), fq)
                } else {
                    warn(fq, stmt, "CREATE INDEX %s blocks writes to %s while it builds; set concurrently: true to build it without blocking", unquoteFQ(m[2]), fq)
                }
            }
            continue
        }
        if m := lockConstraintRe.FindStringSubmatch(stmt); m != nil {
            fq, name := unquoteFQ(m[1]), unquoteFQ(m[2])
            if !exists(fq) { continue }
            notValid := strings.Contains(stmt, " not valid")
            switch m[3] {
            case "foreign key":
                if lt := live.Tables[fq]; !notValid && lt != nil && lt.Partitioned && live.ServerVersion > 0 && live.ServerVersion < 180000 {
                    warn(fq, stmt, "adding foreign key %s checks every row of %s while blocking writes to it and to the referenced table; PostgreSQL %d cannot add it NOT VALID to a partitioned table, so run it when that lock is acceptable", name, fq, live.ServerVersion/10000)
                } else if !notValid {
                    warn(fq, stmt, "adding foreign key %s checks every row of %s while blocking writes to it and to the referenced table; add it with notValid: true and remove notValid in a later migration, which validates it without blocking writes", name, fq)
                }
            case "check":
                if !notValid {
                    warn(fq, stmt, "adding check %s scans all of %s under an ACCESS EXCLUSIVE lock; add it with notValid: true and remove notValid in a later migration, which validates it without blocking writes", name, fq)
                }
            default:
                warn(fq, stmt, "adding %s %s builds its index under an ACCESS EXCLUSIVE lock, blocking reads and writes on %s", m[3], name, fq)
            }
            continue
        }
        if m := lockPrimaryKeyRe.FindStringSubmatch(stmt); m != nil {
            if fq := unquoteFQ(m[1]); exists(fq) {
                warn(fq, stmt, "adding a primary key builds its index under an ACCESS EXCLUSIVE lock, blocking reads and writes on %s", fq)
            }
            continue
        }
        if m := lockNotNullRe.FindStringSubmatch(stmt); m != nil {
            if fq, col := unquoteFQ(m[1]), unquoteFQ(m[2]); exists(fq) && !hasNotNullCheck(live.Tables[fq], col) {
                warn(fq, stmt, "SET NOT NULL on %s.%s scans the whole table under an ACCESS EXCLUSIVE lock; a validated CHECK (%s IS NOT NULL) constraint, added in an earlier migration, lets PostgreSQL skip the scan", fq, col, col)
            }
            continue
        }
        if m := lockTypeRe.FindStringSubmatch(stmt); m != nil {
            fq, col, rest := unquoteFQ(m[1]), unquoteFQ(m[2]), m[3]
            lt := live.Tables[fq]
            if lt == nil || lt.Foreign != nil || lt.Columns[col] == nil { continue }
            to, using := rest, false
            if i := strings.Index(to, " using "); i >= 0 { to, using = to[:i], true }
            if i := strings.Index(to, " collate "); i >= 0 { to = to[:i] }
            if using || typeChangeRewrites(lt.Columns[col].Type, to) {
                warn(fq, stmt, "changing %s.%s from %s to %s rewrites the table and its indexes under an ACCESS EXCLUSIVE lock", fq, col, lt.Columns[col].Type, to)
            }
            continue
        }
        if m := lockLoggedRe.FindStringSubmatch(stmt); m != nil {
            if fq := unquoteFQ(m[1]); exists(fq) {
                warn(fq, stmt, "SET %s rewrites %s under an ACCESS EXCLUSIVE lock", strings.ToUpper(m[2]), fq)
            }
            continue
        }
        if m := lockAddColumnRe.FindStringSubmatch(stmt); m != nil {
            fq, col, def := unquoteFQ(m[1]), unquoteFQ(m[2]), m[3]
            if !exists(fq) { continue }
            if why := addColumnRewrite(def); why != "" {
                warn(fq, stmt, "adding column %s with %s rewrites %s under an ACCESS EXCLUSIVE lock", col, why, fq)
            }
        }
    }
    return out
}

// KeepExisting drops the lock warnings about tables that don't exist in
// live once the renames declared in desired are applied, keeping every other
// warning. Split plans use it so a table created by an earlier step of the
// same plan is not reported.
func KeepExisting(ws []Warning, live *Live, desired *schema.Database) []Warning {
    if len(ws) == 0 { return ws }
    renamed, _ := applyRenames(live.scoped(), desired)
    out := []Warning{}
    for _, w := range ws {
        if w.Kind != "lock" || renamed != nil && (renamed.Tables[w.Table] != nil || renamed.MatViewTables[w.Table] != nil) { out = append(out, w) }
    }
    return out
}

// hasNotNullCheck reports whether lt has a validated CHECK (col IS NOT
// NULL), which lets SET NOT NULL skip its table scan.
func hasNotNullCheck(lt *LiveTable, col string) bool {
    if lt == nil { return false }
    want := normalizeConstraintDef(fmt.Sprintf("CHECK ((%s IS NOT NULL))", printedIdent(col)))
    for _, def := range lt.ConstraintDefs {
        if !strings.HasSuffix(def, "NOT VALID") && normalizeConstraintDef(def) == want { return true }
    }
    return false
}

// addColumnRewrite explains why adding a column with definition def must
// write every existing row, or returns "".
func addColumnRewrite(def string) string {
    lower := strings.ToLower(def)
    first := strings.Fields(lower)
    switch {
    case len(first) > 0 && isSerialType(first[0]):
        return "a serial type"
    case strings.Contains(lower, " as identity"):
        return "an identity"
    case strings.Contains(lower, " generated always as (") && strings.HasSuffix(lower, " stored"):
        return "a stored generated expression"
    }
    if i := strings.Index(lower, " default "); i >= 0 {
        masked, _ := maskStringLiterals(def[i:])
        if volatileCallRe.MatchString(masked) { return "a volatile default" }
    }
    return ""
}

// typeChangeRewrites reports whether ALTER COLUMN TYPE from -> to rewrites
// the table. Widening varchar, numeric and varbit modifiers, dropping them,
// and varchar <-> text are binary coercible; anything else is assumed to
// rewrite.
func typeChangeRewrites(from, to string) bool {
    f, t := normalizeColumnType(from), normalizeColumnType(to)
    if f == t { return false }
    fm, tm := typeModsRe.FindStringSubmatch(f), typeModsRe.FindStringSubmatch(t)
    if fm == nil || tm == nil { return true }
    textual := map[string]bool{"character varying": true, "varchar": true, "text": true}
    if textual[fm[1]] && textual[tm[1]] {
        if tm[1] == "text" || tm[2] == "" { return false }
        return fm[2] == "" || atoi(tm[2]) < atoi(fm[2]) || fm[1] == "text"
    }
    if fm[1] != tm[1] { return true }
    switch fm[1] {
    case "numeric", "decimal":
        if tm[2] == "" { return false }
        return fm[2] == "" || fm[3] != tm[3] || atoi(tm[2]) < atoi(fm[2])
    case "bit varying", "varbit":
        return tm[2] != "" && (fm[2] == "" || atoi(tm[2]) < atoi(fm[2]))
    }
    return true
}

func atoi(s string) int {
    n, _ := strconv.Atoi(s)
    return n
}

// BlockingIndexBuild reports whether w warns about a CREATE INDEX that blocks
// writes to an existing table, which concurrently: true would avoid.
func (w Warning) BlockingIndexBuild() bool {
    return w.Kind == "lock" && lockIndexRe.MatchString(w.Statement)
}

// UnsafeOnly returns a warning (kind "unsafe") for each statement that
// planning desired with --unsafe adds to the plan made with opts, so a run
// without --unsafe reports the changes it left out. The two plans are made
// concurrently. It returns nil when opts.Unsafe is set.
func UnsafeOnly(live *Live, desired *schema.Database, opts Options) []Warning {
    if opts.Unsafe || live == nil || desired == nil { return nil }
    full := make(chan *PlanDiff, 1)
    go func() { full <- planWithUnsafe(live, desired, opts) }()
    safe := PlanWith(live, desired, opts)
    return unsafeOnly(safe, <-full)
}

// PlanReportingUnsafe is PlanWith plus, unless opts.Unsafe is set, the
// warnings of UnsafeOnly; the plan with --unsafe is made concurrently.
func PlanReportingUnsafe(live *Live, desired *schema.Database, opts Options) *PlanDiff {
    if opts.Unsafe || live == nil || desired == nil { return PlanWith(live, desired, opts) }
    full := make(chan *PlanDiff, 1)
    go func() { full <- planWithUnsafe(live, desired, opts) }()
    safe := PlanWith(live, desired, opts)
    safe.Warnings = append(safe.Warnings, unsafeOnly(safe, <-full)...)
    return safe
}

func planWithUnsafe(live *Live, desired *schema.Database, opts Options) *PlanDiff {
    opts.Unsafe = true
    return PlanWith(live, desired, opts)
}

func unsafeOnly(safe, full *PlanDiff) []Warning {
    have := map[string]bool{}
    for _, s := range planStatements(safe) { have[s] = true }
    out := []Warning{}
    for _, s := range planStatements(full) {
        lower := strings.ToLower(strings.TrimSpace(s))
        if have[s] || strings.HasPrefix(lower, "set ") || strings.HasPrefix(lower, "reset ") { continue }
        have[s] = true
        line, _, more := strings.Cut(strings.TrimSpace(s), "\n")
        if more { line += " ..." }
        out = append(out, Warning{Kind: "unsafe", Statement: s, Message: "needs --unsafe, so it was left out: " + line})
    }
    return out
}

func planStatements(p *PlanDiff) []string {
    return append(append(append(append(append([]string{}, p.Prelude...), p.Creates...), p.Alters...), p.Drops...), p.Concurrent...)
}

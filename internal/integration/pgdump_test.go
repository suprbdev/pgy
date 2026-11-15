package integration

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pgDump returns the normalized output of pg_dump --schema-only for
// database db, or "" when PGY_TEST_PGDUMP is unset. PGY_TEST_PGDUMP is the
// pg_dump command line without a database argument; it must match the
// server's major version (CI runs the service container's own pg_dump).
func pgDump(t *testing.T, db string) string {
	t.Helper()
	cmdline := strings.Fields(os.Getenv("PGY_TEST_PGDUMP"))
	if len(cmdline) == 0 {
		return ""
	}
	args := append(cmdline[1:], "--schema-only", "--exclude-schema=pgy", db)
	out, err := exec.Command(cmdline[0], args...).Output()
	if err != nil {
		t.Fatalf("pg_dump %s: %v", db, err)
	}
	return normalizeDump(string(out))
}

var (
	dumpAttverRe = regexp.MustCompile(`pg_attver=\d+(, )?`)
	dumpEmptySet = regexp.MustCompile(`(?m)^ALTER TABLE ONLY \S+ ALTER COLUMN \S+ SET \(\);\n`)
	dumpWithRe   = regexp.MustCompile(`WITH \(([^()]*=[^()]*)\)`)
	dumpTagRe    = regexp.MustCompile(`\$[A-Za-z_]*\$`)
)

// normalizeDump removes what legitimately differs between a database and
// its rebuild: comments, pg_dump's \restrict keys, blank lines, the order of
// WITH (...) options and whitespace just inside dollar-quoted bodies.
func normalizeDump(s string) string {
	s = dumpAttverRe.ReplaceAllString(s, "")
	s = dumpEmptySet.ReplaceAllString(s, "")
	s = dumpWithRe.ReplaceAllStringFunc(s, func(m string) string {
		opts := strings.Split(dumpWithRe.FindStringSubmatch(m)[1], ",")
		for i := range opts {
			opts[i] = strings.TrimSpace(opts[i])
		}
		sort.Strings(opts)
		return "WITH (" + strings.Join(opts, ", ") + ")"
	})
	var b strings.Builder
	for len(s) > 0 {
		loc := dumpTagRe.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			break
		}
		tag := s[loc[0]:loc[1]]
		end := strings.Index(s[loc[1]:], tag)
		if end < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:loc[1]])
		b.WriteString(strings.TrimSpace(s[loc[1] : loc[1]+end]))
		b.WriteString(tag)
		s = s[loc[1]+end+len(tag):]
	}
	lines := []string{}
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "--") || strings.HasPrefix(l, `\restrict`) || strings.HasPrefix(l, `\unrestrict`) {
			continue
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

// dumpDifferences lists the lines only one of two normalized dumps has.
func dumpDifferences(a, b string) []string {
	count := map[string]int{}
	for _, l := range strings.Split(a, "\n") {
		count[l]++
	}
	for _, l := range strings.Split(b, "\n") {
		count[l]--
	}
	out := []string{}
	for _, l := range strings.Split(a, "\n") {
		if count[l] > 0 {
			out = append(out, "- "+l)
			count[l]--
		}
	}
	for _, l := range strings.Split(b, "\n") {
		if count[l] < 0 {
			out = append(out, "+ "+l)
			count[l]++
		}
	}
	return out
}

func TestNormalizeDump(t *testing.T) {
	a := normalizeDump("--\n-- PostgreSQL database dump\n\\restrict abc\nCREATE TABLE t (id int) WITH (fillfactor='80', autovacuum_enabled='false');\n\nCREATE FUNCTION f() RETURNS int\n    AS $$select 1$$;\n")
	b := normalizeDump("\\restrict xyz\nCREATE TABLE t (id int) WITH (autovacuum_enabled='false', fillfactor='80');\nCREATE FUNCTION f() RETURNS int\n    AS $$\nselect 1\n$$;\n")
	if a != b {
		t.Errorf("normalized dumps differ:\n%s\n---\n%s", a, b)
	}
	if d := dumpDifferences("x\ny\ny", "y\nz"); strings.Join(d, "|") != "- x|- y|+ z" {
		t.Errorf("dumpDifferences = %q", d)
	}
}

package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func configAdviceLive() *diff.Live {
	live := memberLive()
	live.Details.ColumnOrder = map[string][]string{}
	live.ExtensionConfig = map[string]string{}
	live.MemberExtension = map[string]string{}
	table := func(fq string, pk bool, cols map[string]string, fks ...string) {
		lt := &diff.LiveTable{Columns: map[string]*diff.LiveColumn{}, Constraints: map[string]bool{}, ConstraintDefs: map[string]string{}}
		for cn, typ := range cols {
			lt.Columns[cn] = &diff.LiveColumn{Type: typ}
			live.Details.ColumnOrder[fq] = append(live.Details.ColumnOrder[fq], cn)
		}
		if pk {
			lt.HasPK = true
			lt.ConstraintDefs["pkey"] = "PRIMARY KEY (id)"
		}
		for i, ref := range fks {
			lt.ConstraintDefs[string(rune('a'+i))+"_fkey"] = "FOREIGN KEY (ref) REFERENCES " + ref + "(id)"
		}
		live.Tables[fq] = lt
		live.ExtensionMembers["rel:"+fq] = true
		live.MemberExtension["rel:"+fq] = "pgcrypto"
		live.ExtensionConfig[fq] = ""
	}
	table("public.hyper", true, map[string]string{"id": "integer", "name": "name"})
	table("public.jobs", true, map[string]string{"id": "integer", "ref": "integer"}, "public.hyper")
	table("public.meta", false, map[string]string{"queue": "text"})
	table("public.chunk", true, map[string]string{"id": "integer", "ref": "integer"}, "public.meta")
	table("public.immv", true, map[string]string{"id": "integer", "relid": "regclass"})
	table("public.logs", true, map[string]string{"id": "integer", "ref": "integer"}, "public.ext_log")
	table("public.ext_log", true, map[string]string{"id": "integer"})
	return live
}

func TestPullConfigurationRowsAdvice(t *testing.T) {
	rows := map[string]int64{"public.hyper": 1, "public.jobs": 2, "public.meta": 1, "public.chunk": 3, "public.immv": 1, "public.logs": 1}
	ext := &schema.External{Relations: []string{"public.ext_*"}}
	res, err := Build(configAdviceLive(), Options{Single: true, ConfigRows: rows, External: ext})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"configuration table public.hyper has 1 row that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. Pull them with --data public.hyper if they are settings to copy; rows that describe objects the extension's functions create at run time (hypertables, partitions, queues) come back when a migration calls those functions instead, and listing the table under external.relations leaves it out of this report",
		"configuration table public.jobs has 2 rows that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. Pull them with --data public.jobs,public.hyper (with the rows they reference) if they are settings to copy;",
		"configuration table public.meta has 1 row that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. --data cannot pull them, since the table has no primary key to identify rows by; rows that describe objects",
		"configuration table public.chunk has 3 rows that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. --data cannot pull them, since it references rows of public.meta, which has no primary key to identify rows by;",
		"configuration table public.immv has 1 row that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. --data cannot pull them, since the table has column relid of type regclass, so its rows name objects of the database;",
		"configuration table public.logs has 1 row that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. --data cannot pull them, since the table references rows of public.ext_log, an external relation;",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}
	if len(res.Warnings) != 6 {
		t.Errorf("want 6 warnings, got:\n%s", warnings)
	}

	delete(rows, "public.jobs")
	res, err = Build(configAdviceLive(), Options{Single: true, ConfigRows: rows, External: ext, Data: map[string][]map[string]any{"public.jobs": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "extension pgcrypto: the rows --data pulls of configuration table public.jobs reference rows of public.hyper, which it does not pull, so a database rebuilt from this YAML rejects them (foreign key a_fkey); pull them together with --data public.jobs,public.hyper"; !strings.Contains(strings.Join(res.Warnings, "\n"), want) {
		t.Errorf("missing %q in:\n%s", want, strings.Join(res.Warnings, "\n"))
	}
}

func TestObjectIdentifierType(t *testing.T) {
	for typ, want := range map[string]bool{"regclass": true, "oid": true, "regprocedure[]": true, "region": false, "integer": false, "text": false} {
		if got := objectIdentifierType(typ); got != want {
			t.Errorf("objectIdentifierType(%q) = %v, want %v", typ, got, want)
		}
	}
}

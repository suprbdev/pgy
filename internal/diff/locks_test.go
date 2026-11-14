package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func lockLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	for cn, typ := range map[string]string{"id": "integer", "name": "character varying(10)", "amount": "numeric(10,2)", "n": "integer", "ref": "integer", "note": "text"} {
		lt.Columns[cn] = &LiveColumn{Type: typ, Nullable: cn != "id"}
	}
	live.Tables["app.t"] = lt
	live.Tables["app.p"] = newLiveTable()
	live.Tables["app.p"].Columns["id"] = &LiveColumn{Type: "integer"}
	return live
}

func lockDB() *schema.Database {
	cols := map[string]*schema.Column{
		"id":     {Type: "int"},
		"name":   {Type: "varchar(20)", Nullable: true},
		"amount": {Type: "numeric(12,2)", Nullable: true},
		"n":      {Type: "bigint", Nullable: true},
		"ref":    {Type: "int"},
		"note":   {Type: "text", Nullable: true},
		"at":     {Type: "timestamptz", Default: "now()", Nullable: true},
		"uid":    {Type: "uuid", Default: "gen_random_uuid()", Nullable: true},
		"twice":  {Type: "int", Generated: "id * 2", Nullable: true},
		"seq":    {Type: "int", Identity: "always"},
	}
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.t": {Name: "t", Columns: cols,
			Indexes: []*schema.Index{
				{Name: "t_note_idx", Columns: []string{"note"}},
				{Name: "t_name_idx", Columns: []string{"name"}, Concurrently: true},
			},
			Constraints: []*schema.Constraint{
				{Name: "t_id_check", Type: "check", Expression: "id > 0"},
				{Name: "t_n_check", Type: "check", Expression: "n > 0", NotValid: true},
				{Name: "t_note_key", Type: "unique", Columns: []string{"note"}},
			},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "t_ref_fkey", Columns: []string{"ref"}, RefTable: "app.p", RefColumns: []string{"id"}},
				{Name: "t_id_fkey", Columns: []string{"id"}, RefTable: "app.p", RefColumns: []string{"id"}, NotValid: true},
			},
		},
		"app.p":   {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PrimaryKey: []string{"id"}},
		"app.new": {Name: "new", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Indexes: []*schema.Index{{Name: "new_id_idx", Columns: []string{"id"}}}},
	}}
}

func TestLockWarnings(t *testing.T) {
	p := PlanWith(lockLive(), lockDB(), Options{Unsafe: true})
	got := []string{}
	for _, w := range p.Warnings {
		got = append(got, w.Message)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		"CREATE INDEX t_note_idx blocks writes to app.t",
		"adding foreign key t_ref_fkey checks every row of app.t",
		"adding check t_id_check scans all of app.t",
		"adding unique t_note_key builds its index",
		"adding a primary key builds its index under an ACCESS EXCLUSIVE lock, blocking reads and writes on app.p",
		"SET NOT NULL on app.t.ref scans the whole table",
		"changing app.t.n from integer to bigint rewrites the table",
		"adding column uid with a volatile default rewrites app.t",
		"adding column twice with a stored generated expression rewrites app.t",
		"adding column seq with an identity rewrites app.t",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
	for _, unwanted := range []string{"t_name_idx", "t_n_check", "t_id_fkey", "app.t.name", "app.t.amount", "column at ", "new_id_idx", "app.new"} {
		if strings.Contains(all, unwanted) {
			t.Errorf("unexpected warning mentioning %q in:\n%s", unwanted, all)
		}
	}
}

func TestTypeChangeRewrites(t *testing.T) {
	for _, c := range []struct {
		from, to string
		rewrite  bool
	}{
		{"character varying(10)", "varchar(20)", false},
		{"character varying(20)", "varchar(10)", true},
		{"character varying(10)", "text", false},
		{"text", "varchar", false},
		{"text", "varchar(5)", true},
		{"numeric(10,2)", "numeric(12,2)", false},
		{"numeric(10,2)", "numeric(12,3)", true},
		{"numeric(10,2)", "numeric", false},
		{"integer", "bigint", true},
		{"integer[]", "bigint[]", true},
	} {
		if got := typeChangeRewrites(c.from, c.to); got != c.rewrite {
			t.Errorf("typeChangeRewrites(%s, %s) = %v, want %v", c.from, c.to, got, c.rewrite)
		}
	}
}

func TestLockWarningsSkipNotNullWithCheck(t *testing.T) {
	live := lockLive()
	live.Tables["app.t"].ConstraintDefs["t_ref_nn"] = "CHECK ((ref IS NOT NULL))"
	live.Tables["app.t"].Constraints["t_ref_nn"] = true
	for _, w := range PlanWith(live, lockDB(), Options{Unsafe: true}).Warnings {
		if strings.Contains(w.Message, "SET NOT NULL") {
			t.Errorf("a validated IS NOT NULL check makes SET NOT NULL cheap: %s", w.Message)
		}
	}
}

func TestConcurrentIndexOnPartitionedTable(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	for _, k := range []string{"app.p", "app.p_eu", "app.p_eu_2024", "app.p_us"} {
		lt := newLiveTable()
		lt.Columns["id"] = &LiveColumn{Type: "integer"}
		live.Tables[k] = lt
	}
	live.Tables["app.p"].Partitioned, live.Tables["app.p_eu"].Partitioned = true, true
	live.Partitions = map[string][]string{"app.p": {"app.p_eu", "app.p_us"}, "app.p_eu": {"app.p_eu_2024"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"app.p": {
		Name: "p", PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"id"}},
		Columns: map[string]*schema.Column{"id": {Type: "int"}},
		Indexes: []*schema.Index{{Name: "p_id_idx", Columns: []string{"id"}, Concurrently: true}},
	}}}
	p := Plan(live, d, false)
	want := []string{
		`create index if not exists "p_id_idx" on only "app"."p"("id");`,
		`create index if not exists "p_eu_p_id_idx" on only "app"."p_eu"("id");`,
		`create index concurrently if not exists "p_eu_2024_p_eu_p_id_idx" on "app"."p_eu_2024"("id");`,
		`alter index "app"."p_eu_p_id_idx" attach partition "app"."p_eu_2024_p_eu_p_id_idx";`,
		`alter index "app"."p_id_idx" attach partition "app"."p_eu_p_id_idx";`,
		`create index concurrently if not exists "p_us_p_id_idx" on "app"."p_us"("id");`,
		`alter index "app"."p_id_idx" attach partition "app"."p_us_p_id_idx";`,
	}
	if strings.Join(p.Concurrent, "\n") != strings.Join(want, "\n") || len(p.Creates) != 0 || len(p.Warnings) != 0 {
		t.Errorf("unexpected plan:\n%s\nwarnings: %+v", Render(p)+strings.Join(p.Concurrent, "\n"), p.Warnings)
	}
	live.Tables["app.p"].Indexes["p_id_idx"] = true
	live.Tables["app.p"].IndexDefs = map[string]*LiveIndex{"p_id_idx": {Method: "btree", Keys: []string{"id"}, Descending: []bool{false}, NullsFirst: []bool{false}, Invalid: true}}
	if p := Plan(live, d, false); strings.Join(p.Concurrent, "\n") != strings.Join(want, "\n") {
		t.Errorf("an invalid partitioned index must be completed again:\n%s", strings.Join(p.Concurrent, "\n"))
	}
	live.Tables["app.p"].IndexDefs["p_id_idx"].Invalid = false
	if p := Plan(live, d, false); len(p.Concurrent) != 0 {
		t.Errorf("a valid index needs nothing:\n%s", strings.Join(p.Concurrent, "\n"))
	}
	d.Tables["app.p"].Indexes[0].Concurrently = false
	live.Tables["app.p"].Indexes = map[string]bool{}
	p = Plan(live, d, false)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "set concurrently: true to build it on each partition") {
		t.Errorf("unexpected warnings: %+v", p.Warnings)
	}
}

func TestInvalidConcurrentIndexRebuilt(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Indexes["t_id_idx"] = true
	lt.IndexDefs = map[string]*LiveIndex{"t_id_idx": {Method: "btree", Keys: []string{"id"}, Descending: []bool{false}, NullsFirst: []bool{false}, Invalid: true}}
	live.Tables["app.t"] = lt
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}},
		Indexes: []*schema.Index{{Name: "t_id_idx", Columns: []string{"id"}, Concurrently: true}},
	}}}
	want := `drop index concurrently if exists "app"."t_id_idx";` + "\n" + `create index concurrently if not exists "t_id_idx" on "app"."t"("id");`
	if got := strings.Join(Plan(live, d, false).Concurrent, "\n"); got != want {
		t.Errorf("an index left invalid by an interrupted build must be rebuilt, got:\n%s", got)
	}
}

func TestNotValidForeignKeyOnPartitionedTableBefore18(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.c": {Name: "c", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PrimaryKey: []string{"id"}},
		"app.o": {Name: "o", PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"at"}},
			Columns:     map[string]*schema.Column{"cid": {Type: "int"}, "at": {Type: "date"}},
			ForeignKeys: []*schema.ForeignKey{{Name: "o_c_fkey", Columns: []string{"cid"}, RefTable: "app.c", RefColumns: []string{"id"}, NotValid: true}}},
	}}
	live := emptyLive()
	live.ServerVersion = 170004
	if p := Plan(live, d, false); !findAlter(p, `alter table "app"."o" add constraint "o_c_fkey" foreign key ("cid") references "app"."c"("id");`) {
		t.Errorf("a new partitioned table gets the foreign key validated:\n%s", Render(p))
	}
	live.Schemas["app"] = true
	for _, k := range []string{"app.c", "app.o"} {
		lt := newLiveTable()
		live.Tables[k] = lt
	}
	live.Tables["app.c"].Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.c"].HasPK = true
	live.Tables["app.c"].Constraints["c_pkey"] = true
	live.Tables["app.o"].Columns["cid"] = &LiveColumn{Type: "integer"}
	live.Tables["app.o"].Columns["at"] = &LiveColumn{Type: "date"}
	live.Tables["app.o"].Partitioned = true
	p := Plan(live, d, false)
	if strings.Contains(Render(p), "o_c_fkey") || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "cannot add a NOT VALID foreign key to a partitioned table") {
		t.Errorf("an existing partitioned table must only warn:\n%s\n%+v", Render(p), p.Warnings)
	}
	live.ServerVersion = 180000
	if p := Plan(live, d, false); !findAlter(p, `alter table "app"."o" add constraint "o_c_fkey" foreign key ("cid") references "app"."c"("id") not valid;`) {
		t.Errorf("PostgreSQL 18 supports NOT VALID here:\n%s", Render(p))
	}
}

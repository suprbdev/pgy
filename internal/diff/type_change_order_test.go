package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func statementOrder(t *testing.T, sql string, stmts ...string) {
	t.Helper()
	last := -1
	for _, s := range stmts {
		i := strings.Index(sql, s)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", s, sql)
		}
		if i < last {
			t.Fatalf("%q comes too early in:\n%s", s, sql)
		}
		last = i
	}
}

func retypedForeignKey() (*Live, *schema.Database) {
	live := liveWithTablePK("public.account", map[string]*LiveColumn{"id": {Type: "bigint"}, "email": {Type: "text"}})
	live.Tables["public.account"].Constraints["account_email_key"] = true
	live.Tables["public.account"].ConstraintDefs = map[string]string{"account_email_key": "UNIQUE (email)"}
	live.Tables["public.session"] = &LiveTable{
		Columns:            map[string]*LiveColumn{"id": {Type: "bigint"}, "account_email": {Type: "text"}},
		Constraints:        map[string]bool{"session_account_fkey": true},
		ConstraintDefs:     map[string]string{"session_account_fkey": "FOREIGN KEY (account_email) REFERENCES account(email)"},
		ConstraintComments: map[string]string{"session_account_fkey": "owner"},
		HasPK:              true,
	}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.account": {Name: "account", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "bigint"}, "email": {Type: "citext"}},
			Constraints: []*schema.Constraint{{Name: "account_email_key", Type: "unique", Columns: []string{"email"}}}},
		"public.session": {Name: "session", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "bigint"}, "account_email": {Type: "citext"}},
			ForeignKeys: []*schema.ForeignKey{{Name: "session_account_fkey", Columns: []string{"account_email"}, RefTable: "public.account", RefColumns: []string{"email"}, Comment: "owner"}}},
	}}
	return live, d
}

func TestTypeChangeDropsForeignKeyRetypedOnBothEnds(t *testing.T) {
	live, d := retypedForeignKey()
	p := Plan(live, d, true)
	statementOrder(t, Render(p),
		`alter table "public"."session" drop constraint "session_account_fkey";`,
		`alter table "public"."account" alter column "email" type citext;`,
		`alter table "public"."session" alter column "account_email" type citext;`,
		`alter table "public"."session" add constraint "session_account_fkey" foreign key ("account_email") references "public"."account"("email") not valid;`,
		`comment on constraint "session_account_fkey" on "public"."session" is 'owner';`)
	if got := RenderConcurrent(p); !strings.Contains(got, `alter table "public"."session" validate constraint "session_account_fkey";`) {
		t.Errorf("the foreign key added again must be validated afterwards, got:\n%s", got)
	}
	if !live.Tables["public.session"].Constraints["session_account_fkey"] {
		t.Error("the live snapshot passed in must not be changed")
	}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("without --unsafe nothing changes, got:\n%s", sql)
	}
}

func TestTypeChangeKeepsForeignKeyRetypedOnOneEnd(t *testing.T) {
	live, d := retypedForeignKey()
	d.Tables["public.account"].Columns["email"].Type = "text"
	sql := Render(Plan(live, d, true))
	if strings.Contains(sql, "session_account_fkey") || !strings.Contains(sql, `alter column "account_email" type citext;`) {
		t.Errorf("a foreign key whose other end keeps its type stays in place, got:\n%s", sql)
	}
}

func retypedIndexedColumn() (*Live, *schema.Database) {
	live := liveWithTablePK("public.users", map[string]*LiveColumn{"id": {Type: "bigint"}, "email": {Type: "citext"}, "path": {Type: "text"}, "note": {Type: "text"}})
	lt := live.Tables["public.users"]
	for _, name := range []string{"users_email_pat", "users_email_plain", "users_note_idx"} {
		lt.Indexes[name] = true
	}
	lt.IndexDefs = map[string]*LiveIndex{
		"users_email_pat":   {Method: "btree", Keys: []string{"email"}, Descending: []bool{false}, NullsFirst: []bool{false}},
		"users_email_plain": {Method: "btree", Keys: []string{"email"}, Descending: []bool{false}, NullsFirst: []bool{false}},
		"users_note_idx":    {Method: "btree", Keys: []string{"note"}, Descending: []bool{false}, NullsFirst: []bool{false}},
	}
	lt.IndexOpclasses = map[string][]LiveIndexOpclass{
		"users_email_pat":   {{Name: "citext_pattern_ops"}},
		"users_email_plain": {{Name: "citext_ops", Default: true}},
		"users_note_idx":    {{Name: "text_ops", Default: true}},
	}
	lt.IndexComments = map[string]string{"users_email_pat": "prefix search"}
	lt.Constraints["users_path_check"], lt.Constraints["users_note_check"] = true, true
	lt.ConstraintDefs = map[string]string{"users_path_check": "CHECK ((path <> ''::text))", "users_note_check": "CHECK ((note <> ''::text))"}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.users": {Name: "users", PrimaryKey: []string{"id"},
		Columns: map[string]*schema.Column{"id": {Type: "bigint"}, "email": {Type: "text"}, "path": {Type: "ltree", Using: "text2ltree(path)"}, "note": {Type: "text"}},
		Indexes: []*schema.Index{
			{Name: "users_email_pat", Columns: []string{"email"}, Opclass: "text_pattern_ops", Comment: "prefix search"},
			{Name: "users_email_plain", Columns: []string{"email"}},
			{Name: "users_note_idx", Columns: []string{"note"}, Unique: true},
		},
		Constraints: []*schema.Constraint{
			{Name: "users_path_check", Type: "check", Expression: "nlevel(path) > 0"},
			{Name: "users_note_check", Type: "check", Expression: "note <> 'x'"},
		},
	}}}
	return live, d
}

func TestTypeChangeDropsReplacedIndexAndConstraintFirst(t *testing.T) {
	live, d := retypedIndexedColumn()
	p := Plan(live, d, true)
	sql := Render(p)
	statementOrder(t, sql,
		`drop index "public"."users_email_pat";`,
		`alter table "public"."users" drop constraint "users_path_check";`,
		`alter table "public"."users" alter column "email" type text;`,
		`alter table "public"."users" alter column "path" type ltree using text2ltree(path);`,
		`create index if not exists "users_email_pat" on "public"."users"("email" text_pattern_ops);`,
		`alter table "public"."users" add constraint "users_path_check" check (nlevel(path) > 0) not valid;`)
	statementOrder(t, sql,
		`alter table "public"."users" alter column "path" type ltree`,
		`drop index "public"."users_note_idx";`,
		`alter table "public"."users" drop constraint "users_note_check";`)
	for _, once := range []string{`drop index "public"."users_email_pat";`, `drop constraint "users_path_check";`, `comment on index "public"."users_email_pat" is 'prefix search';`} {
		if n := strings.Count(sql, once); n != 1 {
			t.Errorf("want %q once, got %d in:\n%s", once, n, sql)
		}
	}
	if strings.Contains(sql, "users_email_plain") {
		t.Errorf("an index the YAML leaves as it is stays for PostgreSQL to rebuild, got:\n%s", sql)
	}
	if !strings.Contains(RenderConcurrent(p), `validate constraint "users_path_check";`) {
		t.Errorf("the check added again must be validated afterwards, got:\n%s", RenderConcurrent(p))
	}
}

func TestTypeChangePrunesIndexAndConstraintFirst(t *testing.T) {
	live, d := retypedIndexedColumn()
	dt := d.Tables["public.users"]
	dt.Indexes, dt.Constraints = dt.Indexes[1:], dt.Constraints[1:]
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	statementOrder(t, sql,
		`drop index "public"."users_email_pat";`,
		`alter table "public"."users" drop constraint "users_path_check";`,
		`alter table "public"."users" alter column "email" type text;`)
	if strings.Count(sql, "users_email_pat") != 1 || strings.Count(sql, "users_path_check") != 1 {
		t.Errorf("pruned objects are dropped once, before the type change, got:\n%s", sql)
	}
	if sql := Render(Plan(live, d, true)); strings.Contains(sql, "users_email_pat") || strings.Contains(sql, "users_path_check") {
		t.Errorf("without --prune undeclared objects are left alone, got:\n%s", sql)
	}
}

func TestTypeChangeResetsDefault(t *testing.T) {
	cases := []struct {
		name string
		live *LiveColumn
		want *schema.Column
		sql  string
	}{
		{"unchanged default", &LiveColumn{Type: "text", Default: "''::text"}, &schema.Column{Type: "hstore", Default: "''", Using: "attrs::hstore"},
			`alter table "public"."t" alter column "attrs" drop default;|alter table "public"."t" alter column "attrs" type hstore using attrs::hstore;|alter table "public"."t" alter column "attrs" set default '';`},
		{"changed default", &LiveColumn{Type: "hstore", Default: "''::hstore"}, &schema.Column{Type: "jsonb", Default: "'{}'::jsonb", Using: "hstore_to_jsonb(attrs)"},
			`alter table "public"."t" alter column "attrs" drop default;|alter table "public"."t" alter column "attrs" type jsonb using hstore_to_jsonb(attrs);|alter table "public"."t" alter column "attrs" set default '{}'::jsonb;`},
		{"removed default", &LiveColumn{Type: "text", Default: "''::text"}, &schema.Column{Type: "hstore", Using: "attrs::hstore"},
			`alter table "public"."t" alter column "attrs" drop default;|alter table "public"."t" alter column "attrs" type hstore using attrs::hstore;`},
		{"new default", &LiveColumn{Type: "text"}, &schema.Column{Type: "hstore", Default: "''", Using: "attrs::hstore"},
			`alter table "public"."t" alter column "attrs" type hstore using attrs::hstore;|alter table "public"."t" alter column "attrs" set default '';`},
		{"sequence default", &LiveColumn{Type: "integer", Default: "nextval('t_attrs_seq'::regclass)"}, &schema.Column{Type: "bigint"},
			`alter table "public"."t" alter column "attrs" type bigint;`},
		{"collation only", &LiveColumn{Type: "text", Default: "''::text"}, &schema.Column{Type: "text", Default: "''", Collate: "C"},
			`alter table "public"."t" alter column "attrs" type text collate "C";`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.live.Nullable, tc.want.Nullable = true, true
			got := strings.Join(alterColumnStmts("public.t", "attrs", tc.want, tc.live, nil, true, 170000, "", func(string) {}), "|")
			if got != tc.sql {
				t.Errorf("\n got %s\nwant %s", got, tc.sql)
			}
		})
	}
	safe := strings.Join(alterColumnStmts("public.t", "attrs", &schema.Column{Type: "jsonb", Default: "'{}'::jsonb", Nullable: true}, &LiveColumn{Type: "hstore", Default: "''::hstore", Nullable: true}, nil, false, 170000, "", func(string) {}), "|")
	if safe != `alter table "public"."t" alter column "attrs" set default '{}'::jsonb;` {
		t.Errorf("without --unsafe only the changed default is set, got %s", safe)
	}
}

func retypedGeneratedSource(generated *schema.Column, virtual bool) (*Live, *schema.Database) {
	live := liveWithTablePK("app.users", map[string]*LiveColumn{"id": {Type: "integer"}, "name": {Type: "text"}, "name_lc": {Type: "text", Generated: "lower(name)", Virtual: virtual, Nullable: true}})
	live.Schemas["app"] = true
	live.ColumnDependents = map[string][]ColumnDependent{"app.users.name": {{Kind: "generated", Name: "name_lc", Table: "app.users"}}}
	cols := map[string]*schema.Column{"id": {Type: "integer"}, "name": {Type: "citext"}}
	if generated != nil {
		cols["name_lc"] = generated
	}
	return live, &schema.Database{Tables: map[string]*schema.Table{"app.users": {Name: "users", PrimaryKey: []string{"id"}, Columns: cols}}}
}

func TestTypeChangeReaddsGeneratedColumn(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		live, d := retypedGeneratedSource(&schema.Column{Type: "text", Generated: "lower(name)", Virtual: virtual, Nullable: true}, virtual)
		p := Plan(live, d, true)
		kind := "stored"
		if virtual {
			kind = "virtual"
		}
		statementOrder(t, Render(p),
			`alter table "app"."users" drop column "name_lc";`,
			`alter table "app"."users" alter column "name" type citext;`,
			`alter table "app"."users" add column "name_lc" text generated always as (lower(name)) `+kind+`;`)
		notes, rewrite := 0, false
		for _, w := range p.Warnings {
			if w.Message == "generated column app.users.name_lc is dropped and added again at the end of the table, because it uses app.users.name, whose type changes; the indexes, constraints and statistics on it, and the views, triggers, policies and rules that use it, are created again" {
				notes++
			}
			rewrite = rewrite || w.Kind == "lock" && strings.Contains(w.Message, "adding column name_lc with a stored generated expression rewrites app.users")
			if w.Kind == "dependents" {
				t.Errorf("virtual %v: nothing is in the way: %v", virtual, w)
			}
		}
		if notes != 1 || rewrite == virtual {
			t.Errorf("virtual %v: want one note, and a rewrite warning for a stored column, got %v", virtual, p.Warnings)
		}
		if ws := Plan(live, d, false).Warnings; len(ws) != 0 {
			t.Errorf("virtual %v: without --unsafe the type doesn't change, so nothing is reported, got %v", virtual, ws)
		}
	}
}

func generatedWithDependents() (*Live, *schema.Database) {
	live, d := retypedGeneratedSource(nil, false)
	live.Tables["app.users"].Columns = map[string]*LiveColumn{
		"id":   {Type: "integer"},
		"name": {Type: "text"},
		"a_lc": {Type: "text", Generated: "lower(name)", Nullable: true, Comment: "unmanaged", StatisticsTarget: new(int), Storage: "main", TypeStorage: "extended", Options: map[string]string{"n_distinct": "-0.5"}},
	}
	*live.Tables["app.users"].Columns["a_lc"].StatisticsTarget = 500
	live.ColumnDependents = map[string][]ColumnDependent{
		"app.users.name": {{Kind: "generated", Name: "a_lc", Table: "app.users"}},
		"app.users.a_lc": {{Kind: "view", Name: "app.lc", Table: "app.lc"}},
	}
	live.Views = map[string]bool{"app.lc": true}
	lt := live.Tables["app.users"]
	lt.Indexes = map[string]bool{"users_a_lc_idx": true, "users_a_lc_key": true}
	lt.IndexDefs = map[string]*LiveIndex{"users_a_lc_idx": {Method: "btree", Keys: []string{"a_lc"}}, "users_a_lc_key": {Unique: true, Method: "btree", Keys: []string{"a_lc"}, Constraint: true}}
	lt.Constraints = map[string]bool{"users_a_lc_key": true, "users_a_lc_check": true}
	lt.ConstraintDefs = map[string]string{"users_a_lc_key": "UNIQUE (a_lc)", "users_a_lc_check": "CHECK ((a_lc <> ''::text))"}
	lt.Statistics = map[string]*LiveStatistic{"users_stats": {Items: []string{"id", "a_lc"}, Target: -1}}
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.users": {"a_lc": {"reader": {"select": true}}}}
	dt := d.Tables["app.users"]
	dt.Columns["a_lc"] = &schema.Column{Type: "text", Generated: "lower(name)", Nullable: true}
	dt.Indexes = []*schema.Index{{Name: "users_a_lc_idx", Columns: []string{"a_lc"}}}
	dt.Constraints = []*schema.Constraint{{Name: "users_a_lc_key", Type: "unique", Columns: []string{"a_lc"}}, {Name: "users_a_lc_check", Type: "check", Expression: "a_lc <> ''"}}
	dt.Statistics = []*schema.Statistic{{Name: "users_stats", Columns: []string{"id", "a_lc"}}}
	d.Views = map[string]*schema.View{"app.lc": {Name: "lc", Query: "select a_lc from app.users", DependsOn: []string{"table app.users"}}}
	return live, d
}

func TestTypeChangeReaddsGeneratedColumnWithWhatIsOnIt(t *testing.T) {
	live, d := generatedWithDependents()
	p := Plan(live, d, true)
	sql := Render(p)
	statementOrder(t, sql,
		`drop view "app"."lc";`,
		`alter table "app"."users" drop column "a_lc";`,
		`alter table "app"."users" alter column "name" type citext;`,
		`alter table "app"."users" add column "a_lc" text generated always as (lower(name)) stored;`,
		`create or replace view "app"."lc" as select a_lc from app.users;`)
	for _, want := range []string{
		`create index if not exists "users_a_lc_idx" on "app"."users"`,
		`alter table "app"."users" add constraint "users_a_lc_key" unique ("a_lc");`,
		`alter table "app"."users" add constraint "users_a_lc_check" check (a_lc <> '') not valid;`,
		`create statistics "app"."users_stats" on "id", "a_lc" from "app"."users";`,
		`alter table "app"."users" alter column "a_lc" set storage main;`,
		`alter table "app"."users" alter column "a_lc" set statistics 500;`,
		`alter table "app"."users" alter column "a_lc" set (n_distinct = -0.5);`,
		`comment on column "app"."users"."a_lc" is 'unmanaged';`,
		`grant select ("a_lc") on table "app"."users" to "reader";`,
	} {
		if strings.Count(sql, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "drop index") || strings.Contains(sql, "drop constraint") || strings.Contains(sql, "drop statistics") {
		t.Errorf("what is on the column goes with it, got:\n%s", sql)
	}
	if !live.Tables["app.users"].Indexes["users_a_lc_idx"] || live.Tables["app.users"].Columns["a_lc"] == nil {
		t.Error("the live snapshot passed in must not be changed")
	}

	c := d.Tables["app.users"].Columns["a_lc"]
	c.Comment, c.Storage, c.StatisticsTarget, c.Grants = "declared", "extended", new(int), map[string][]string{}
	*c.StatisticsTarget = -1
	sql = Render(Plan(live, d, true))
	for _, unwanted := range []string{"unmanaged", "set storage main", "set statistics 500", `to "reader"`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("%q is managed by the YAML and must not be restored, got:\n%s", unwanted, sql)
		}
	}
	if !strings.Contains(sql, `comment on column "app"."users"."a_lc" is 'declared';`) {
		t.Errorf("the declared comment is set, got:\n%s", sql)
	}
}

func TestTypeChangeKeepsGeneratedColumnItCannotReadd(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Live, *schema.Database)
		reason string
	}{
		"undeclared index": {func(_ *Live, d *schema.Database) { d.Tables["app.users"].Indexes = nil }, "index users_a_lc_idx on it isn't declared"},
		"undeclared constraint": {func(_ *Live, d *schema.Database) {
			d.Tables["app.users"].Constraints = d.Tables["app.users"].Constraints[:1]
		}, "constraint users_a_lc_check on it isn't declared"},
		"undeclared statistics": {func(_ *Live, d *schema.Database) { d.Tables["app.users"].Statistics = nil }, "statistics users_stats on it isn't declared"},
		"undeclared view":       {func(_ *Live, d *schema.Database) { delete(d.Views, "app.lc") }, "view app.lc uses it and isn't declared"},
		"primary key": {func(l *Live, d *schema.Database) {
			d.Tables["app.users"].PrimaryKey = []string{"id", "a_lc"}
		}, "it is part of the primary key"},
		"referenced": {func(l *Live, _ *schema.Database) {
			l.Tables["app.other"] = &LiveTable{Columns: map[string]*LiveColumn{"lc": {Type: "text"}}, ConstraintDefs: map[string]string{"other_lc_fkey": "FOREIGN KEY (lc) REFERENCES app.users(a_lc)"}}
		}, "foreign key other_lc_fkey on app.other references it"},
		"published": {func(l *Live, _ *schema.Database) {
			l.Publications = map[string]*LivePublication{"pub": {Tables: map[string]LivePublicationTable{"app.users": {Columns: []string{"id", "a_lc"}}}}}
		}, "publication pub lists it"},
		"partitioned": {func(l *Live, _ *schema.Database) { l.Tables["app.users"].Partitioned = true }, "app.users is partitioned"},
		"inherited": {func(l *Live, _ *schema.Database) {
			l.Tables["app.child"] = &LiveTable{Columns: map[string]*LiveColumn{}, Inherits: []string{"app.users"}}
		}, "app.users has inheritance children"},
		"collation": {func(l *Live, _ *schema.Database) { l.Tables["app.users"].Columns["a_lc"].Collation = "C" }, "its collation C isn't declared"},
	} {
		live, d := generatedWithDependents()
		tc.change(live, d)
		p := Plan(live, d, true)
		want := "changing the type of app.users.name: generated column a_lc uses it, and PostgreSQL rejects the change while it does; pgy can't drop a_lc and add it again, because " + tc.reason + ", so do that in a hand-written migration"
		found := false
		for _, w := range p.Warnings {
			found = found || w.Kind == "dependents" && w.Message == want
		}
		if sql := Render(p); !found || strings.Contains(sql, `drop column "a_lc"`) || strings.Contains(sql, `add column "a_lc"`) {
			t.Errorf("%s: want %q and the column left alone, got %v\n%s", name, want, p.Warnings, sql)
		}
	}

	live, d := generatedWithDependents()
	d.Tables["app.users"].Indexes = nil
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if sql := Render(p); !strings.Contains(sql, `add column "a_lc"`) || strings.Contains(sql, "users_a_lc_idx") {
		t.Errorf("with --prune an undeclared index goes with the column and isn't created again, got:\n%s", sql)
	}
}

func TestTypeChangeDropsExpressionOfGeneratedColumnTheYAMLGivesUp(t *testing.T) {
	live, d := retypedGeneratedSource(nil, false)
	p := Plan(live, d, true)
	statementOrder(t, Render(p),
		`alter table "app"."users" alter column "name_lc" drop expression;`,
		`alter table "app"."users" alter column "name" type citext;`,
		`alter table "app"."users" drop column "name_lc";`)

	live, d = retypedGeneratedSource(&schema.Column{Type: "text", Nullable: true}, false)
	p = Plan(live, d, true)
	sql := Render(p)
	statementOrder(t, sql,
		`alter table "app"."users" alter column "name_lc" drop expression;`,
		`alter table "app"."users" alter column "name" type citext;`)
	if strings.Count(sql, "drop expression") != 1 || strings.Contains(sql, "drop column") || len(p.Warnings) != 1 {
		t.Errorf("a generated column declared plain loses its expression once and stays, got %v\n%s", p.Warnings, sql)
	}

	live, d = retypedGeneratedSource(nil, true)
	p = Plan(live, d, true)
	if sql := Render(p); strings.Contains(sql, "drop expression") || len(p.Warnings) < 2 {
		t.Errorf("a virtual generated column has no expression to drop, so it is reported, got %v\n%s", p.Warnings, sql)
	}
}

func TestTypeChangeDropsReplacedForeignKeyFirst(t *testing.T) {
	live, d := retypedForeignKey()
	d.Tables["public.account"].Columns["email"].Type = "text"
	d.Tables["public.session"].ForeignKeys[0].OnDelete = "cascade"
	sql := Render(Plan(live, d, true))
	statementOrder(t, sql,
		`alter table "public"."session" drop constraint "session_account_fkey";`,
		`alter table "public"."session" alter column "account_email" type citext;`,
		`alter table "public"."session" add constraint "session_account_fkey" foreign key ("account_email") references "public"."account"("email") on delete cascade not valid;`)
	if n := strings.Count(sql, `drop constraint "session_account_fkey"`); n != 1 {
		t.Errorf("want the foreign key dropped once, got %d in:\n%s", n, sql)
	}
}

func TestTypeChangeKeepsForeignKeyItCouldNotAddAgain(t *testing.T) {
	live, d := retypedForeignKey()
	live.ServerVersion = 170000
	d.Tables["public.session"].PartitionBy = &schema.PartitionBy{Type: "hash", Columns: []string{"id"}}
	d.Tables["public.session"].ForeignKeys[0].NotValid = true
	live.Tables["public.session"].ConstraintDefs["session_account_fkey"] += " NOT VALID"
	if sql := Render(Plan(live, d, true)); strings.Contains(sql, "session_account_fkey") {
		t.Errorf("PostgreSQL 17 can't add a NOT VALID foreign key to a partitioned table, so it must stay, got:\n%s", sql)
	}
	live.ServerVersion = 180000
	if sql := Render(Plan(live, d, true)); !strings.Contains(sql, `drop constraint "session_account_fkey"`) {
		t.Errorf("PostgreSQL 18 can add it again, got:\n%s", sql)
	}
}

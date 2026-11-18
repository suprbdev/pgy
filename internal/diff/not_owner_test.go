package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// notOwnerLive is a database where the connecting role app has its own
// privileges only, and platform owns the table public.q, the function
// public.f(), the view public.pv and the schema platform.
func notOwnerLive() *Live {
	live := liveWithTable("public.q", map[string]*LiveColumn{"id": {Type: "integer"}, "ttl": {Type: "interval", Nullable: true, Default: "'01:00:00'::interval"}})
	live.Tables["public.mine"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}, "ttl": {Type: "interval", Nullable: true, Default: "'01:00:00'::interval"}},
		Constraints: map[string]bool{}, Indexes: map[string]bool{}, Triggers: map[string]string{}}
	live.Schemas["public"], live.Schemas["platform"] = true, true
	live.CurrentUser = "app"
	live.OwnerRoles = map[string]bool{"app": true}
	live.Roles["reader"] = true
	live.RelComments = map[string]string{}
	live.SchemaComments = map[string]string{}
	live.TypeGrants = map[string]map[string]map[string]bool{}
	live.Functions["public.f()"] = true
	norm := normalizeFunctionSignature("public.f()")
	live.FunctionDefs[norm] = &LiveFunction{Signature: "public.f()", Kind: "f", Body: "select 1", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Language: "sql", Returns: "integer"}
	live.FunctionPublicExec[norm] = true
	live.Views["public.pv"] = true
	live.Owners = map[string]string{
		"rel:public.q": "platform", "rel:public.mine": "app", "rel:public.pv": "platform",
		"proc:" + norm: "platform", "schema:platform": "platform", "schema:public": "app",
	}
	return live
}

func notOwnerTable(name, ttlDefault string) *schema.Table {
	return &schema.Table{Name: name, ColumnsSet: true, ColumnOrder: []string{"id", "ttl"}, Columns: map[string]*schema.Column{
		"id":  {Type: "integer"},
		"ttl": {Type: "interval", Nullable: true, Default: ttlDefault},
	}}
}

func notOwnerWarnings(p *PlanDiff) []string {
	var out []string
	for _, w := range p.Warnings {
		if w.Kind == "not_owner" {
			out = append(out, w.Message)
		}
	}
	return out
}

func TestNotOwnerAlterLeftOut(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.q":    notOwnerTable("public.q", "'2 hours'::interval"),
		"public.mine": notOwnerTable("public.mine", "'2 hours'::interval"),
	}}
	p := PlanWith(notOwnerLive(), d, Options{})
	wantSQL(t, Render(p), `alter table "public"."mine" alter column "ttl" set default '2 hours'::interval;`)
	got := notOwnerWarnings(p)
	if len(got) != 1 || !strings.HasPrefix(got[0], "table public.q is owned by platform, and app, the connecting role, does not have that role's privileges, so the plan leaves out the statement that needs them (alter table \"public\".\"q\" alter column \"ttl\" set default '2 hours'::interval;); have platform run it") {
		t.Errorf("want one not_owner warning for public.q, got %q", got)
	}
}

func TestNotOwnerSuperuserAndOfflineKeepEverything(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": notOwnerTable("public.q", "'2 hours'::interval")}}
	for name, adjust := range map[string]func(*Live){
		"superuser":  func(l *Live) { l.OwnerRoles["platform"] = true },
		"member":     func(l *Live) { l.OwnerRoles["platform"] = true; l.CurrentUser = "member_of_platform" },
		"unknown":    func(l *Live) { l.OwnerRoles = nil },
		"no owner":   func(l *Live) { delete(l.Owners, "rel:public.q") },
		"from-empty": func(l *Live) { l.Offline = true },
	} {
		live := notOwnerLive()
		adjust(live)
		p := PlanWith(live, d, Options{})
		if !strings.Contains(Render(p), `alter table "public"."q" alter column "ttl" set default`) || len(notOwnerWarnings(p)) > 0 {
			t.Errorf("%s: the statement stays:\n%s\n%v", name, Render(p), notOwnerWarnings(p))
		}
	}
}

func TestNotOwnerWholeObjectGroupLeftOut(t *testing.T) {
	q := notOwnerTable("public.q", "'01:00:00'::interval")
	q.Columns["note"] = &schema.Column{Type: "text", Nullable: true, Comment: "a note"}
	q.Comment = "platform table"
	q.Indexes = []*schema.Index{{Name: "q_ttl_idx", Columns: []string{"ttl"}}}
	q.Grants = map[string][]string{"reader": {"select"}}
	q.Data = &schema.TableData{Key: []string{"id"}, Rows: []map[string]any{{"id": 1, "note": "x"}}}
	mine := notOwnerTable("public.mine", "'01:00:00'::interval")
	mine.Comment = "mine"
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": q, "public.mine": mine}}
	p := PlanWith(notOwnerLive(), d, Options{})
	sql := Render(p) + RenderConcurrent(p)
	if strings.Contains(sql, `"public"."q"`) || strings.Contains(sql, `"q_ttl_idx"`) {
		t.Errorf("everything the plan does for public.q goes:\n%s", sql)
	}
	if !strings.Contains(sql, `comment on table "public"."mine" is 'mine';`) {
		t.Errorf("the connecting role's own table is changed:\n%s", sql)
	}
	got := notOwnerWarnings(p)
	if len(got) != 1 || !strings.Contains(got[0], "table public.q is owned by platform") || !strings.Contains(got[0], "statements that need them") {
		t.Errorf("one warning for the table, got %q", got)
	}
}

func TestNotOwnerRoutineViewSchemaLeftOut(t *testing.T) {
	d := &schema.Database{
		Functions:      map[string]*schema.Function{"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "integer", Language: "sql", Body: "select 2", Comment: "two"}},
		Views:          map[string]*schema.View{"public.pv": {Name: "pv", Query: "select 1 as one", Replace: true, Comment: "v"}},
		SchemaComments: map[string]string{"platform": "the platform's"},
		Tables:         map[string]*schema.Table{"public.mine": notOwnerTable("public.mine", "'01:00:00'::interval")},
	}
	p := PlanWith(notOwnerLive(), d, Options{})
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		t.Errorf("nothing the connecting role may run is left:\n%s", sql)
	}
	got := strings.Join(notOwnerWarnings(p), "\n")
	for _, want := range []string{"function public.f() is owned by platform", "view public.pv is owned by platform", "schema platform is owned by platform"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestNotOwnerGrantOption(t *testing.T) {
	q := notOwnerTable("public.q", "'01:00:00'::interval")
	q.Grants = map[string][]string{"reader": {"select"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": q}}
	live := notOwnerLive()
	live.TableGrants["public.q"] = map[string]map[string]bool{"app": {"select*": true}}
	q.Grants["app"] = []string{"select*"}
	p := PlanWith(live, d, Options{})
	wantSQL(t, Render(p), `grant select on table "public"."q" to "reader";`)
	if got := notOwnerWarnings(p); len(got) > 0 {
		t.Errorf("a grant option is enough for a GRANT: %v", got)
	}

	live.TableGrants["public.q"] = map[string]map[string]bool{"app": {"select": true}}
	q.Grants["app"] = []string{"select"}
	p = PlanWith(live, d, Options{})
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		t.Errorf("without the grant option the GRANT goes:\n%s", sql)
	}
	if got := notOwnerWarnings(p); len(got) != 1 || !strings.Contains(got[0], "has neither that role's privileges nor the grant options the statements need") {
		t.Errorf("want a warning naming the grant option, got %q", got)
	}
}

func TestNotOwnerTriggerPrivilege(t *testing.T) {
	q := notOwnerTable("public.q", "'01:00:00'::interval")
	q.Triggers = []*schema.Trigger{{Name: "q_touch", Timing: "before", Events: []string{"update"}, Level: "row", Procedure: "public.touch()"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": q}}
	live := notOwnerLive()
	live.TableGrants["public.q"] = map[string]map[string]bool{"app": {"trigger": true}}
	if sql := Render(PlanWith(live, d, Options{})); !strings.Contains(sql, `create trigger "q_touch"`) {
		t.Errorf("the TRIGGER privilege is enough for CREATE TRIGGER:\n%s", sql)
	}
	live.TableGrants["public.q"] = nil
	p := PlanWith(live, d, Options{})
	if strings.Contains(Render(p), "create trigger") || len(notOwnerWarnings(p)) != 1 {
		t.Errorf("without it the trigger is left out with a warning:\n%s\n%v", Render(p), notOwnerWarnings(p))
	}
}

func TestNotOwnerExternalRoleWarnsForDeclaredChanges(t *testing.T) {
	live := notOwnerLive()
	live.Schemas["storage"] = true
	live.Tables["storage.objects"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Constraints: map[string]bool{}, Indexes: map[string]bool{}, Triggers: map[string]string{"platform_trigger": "CREATE TRIGGER platform_trigger ..."}, Policies: map[string]*LivePolicy{}}
	live.Tables["public.platform_log"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Constraints: map[string]bool{}, Indexes: map[string]bool{}, Triggers: map[string]string{}}
	live.Owners["rel:storage.objects"], live.Owners["rel:public.platform_log"] = "platform", "platform"
	live.Tables["public.q"].Triggers = map[string]string{"platform_q_trigger": "CREATE TRIGGER platform_q_trigger ..."}
	q := notOwnerTable("public.q", "'2 hours'::interval")
	q.Grants = map[string][]string{"reader": {"select"}}
	q.Data = &schema.TableData{Key: []string{"id"}, Rows: []map[string]any{{"id": 1}}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.q":        q,
		"storage.objects": {Name: "storage.objects", Policies: []*schema.Policy{{Name: "own_rows", Using: "true"}}},
		"public.mine":     notOwnerTable("public.mine", "'01:00:00'::interval"),
	}}
	ext := &schema.External{Roles: []string{"plat*"}, Schemas: []string{"storage"}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: ext})
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		t.Errorf("nothing the connecting role may run is left:\n%s", sql)
	}
	got := notOwnerWarnings(p)
	want := []string{
		"table public.q is owned by platform, which the config's external.roles lists, and app, the connecting role, does not have that role's privileges, so the plan leaves out the 3 statements that need them",
		"table storage.objects is owned by platform, which the config's external.roles lists, and app, the connecting role, does not have that role's privileges, so the plan leaves out the statement that needs them (create policy",
	}
	if len(got) != len(want) {
		t.Fatalf("a change the YAML asks for on an external role's object is reported, what --prune leaves out of it isn't; got %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("warning %d:\n got %q\nwant %q...", i, got[i], want[i])
		}
	}
	for _, w := range got {
		if strings.Contains(w, "platform_log") || strings.Contains(w, "--prune") {
			t.Errorf("--prune leaves an external role's objects alone without a word, got %q", w)
		}
	}
}

func TestNotOwnerDatabaseAndExtension(t *testing.T) {
	live := notOwnerLive()
	live.DatabaseOwner = "platform"
	live.DatabaseConfig = map[string]string{}
	live.Extensions["hstore"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"hstore": {Schema: "public", Version: "1.7", Owner: "platform", Relocatable: true, UpdateTargets: map[string]bool{"1.8": true}}}
	d := &schema.Database{
		DatabaseSettings: map[string]string{"search_path": `"$user", public, extensions`, "work_mem": "8MB"},
		Extensions:       []*schema.Extension{{Name: "hstore", Schema: "public", Version: "1.8", Comment: "kv"}},
	}
	p := PlanWith(live, d, Options{})
	if sql := strings.TrimSpace(Render(p) + RenderExtensionUpdates(p)); sql != "" {
		t.Errorf("settings of a database and an extension the role doesn't own are left out, with the session's search_path:\n%s", sql)
	}
	got := strings.Join(notOwnerWarnings(p), "\n")
	for _, want := range []string{"the database is owned by platform", "extension hstore is owned by platform"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func retypeLive() (*Live, *schema.Database) {
	live := notOwnerLive()
	live.Schemas["app"] = true
	live.Tables["app.t"] = &LiveTable{Columns: map[string]*LiveColumn{"b": {Type: "integer"}}, Constraints: map[string]bool{}, Indexes: map[string]bool{}, Triggers: map[string]string{}}
	live.Views["app.v"] = true
	live.Owners["rel:app.t"], live.Owners["rel:app.v"] = "app", "app"
	live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
	d := &schema.Database{
		Tables: map[string]*schema.Table{
			"app.t":       {Name: "t", ColumnsSet: true, Columns: map[string]*schema.Column{"b": {Type: "bigint"}}},
			"public.mine": notOwnerTable("public.mine", "'01:00:00'::interval"),
		},
		Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select b from app.t"}},
	}
	return live, d
}

func TestNotOwnerRetypedColumnWithForeignView(t *testing.T) {
	live, d := retypeLive()
	if sql := Render(PlanWith(live, d, Options{Unsafe: true})); !strings.Contains(sql, `drop view "app"."v";`) || !strings.Contains(sql, `alter table "app"."t" alter column "b" type bigint`) {
		t.Fatalf("the view is dropped and created again around the type change:\n%s", sql)
	}
	live.Owners["rel:app.v"] = "platform"
	p := PlanWith(live, d, Options{Unsafe: true})
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		t.Errorf("a type change that needs a view the role doesn't own dropped is left out with it:\n%s", sql)
	}
	got := notOwnerWarnings(p)
	if len(got) != 1 || !strings.HasPrefix(got[0], "view app.v is owned by platform") || !strings.Contains(got[0], "and with them the statement for table app.t, which can't go without them; have platform run them") {
		t.Errorf("want one warning naming the view and the table, got %q", got)
	}
}

func TestNotOwnerRetypedForeignTableKeepsItsView(t *testing.T) {
	live, d := retypeLive()
	live.Owners["rel:app.t"] = "platform"
	p := PlanWith(live, d, Options{Unsafe: true})
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		t.Errorf("the view isn't dropped and created again for a type change that is left out:\n%s", sql)
	}
	if got := notOwnerWarnings(p); len(got) != 1 || !strings.HasPrefix(got[0], "table app.t is owned by platform") || !strings.Contains(got[0], "view app.v") {
		t.Errorf("want one warning naming the table and the view, got %q", got)
	}
}

func TestNotOwnerPruneKeepsViewsOfForeignDependents(t *testing.T) {
	live := notOwnerLive()
	live.Schemas["app"] = true
	live.Tables["app.t"] = &LiveTable{Columns: map[string]*LiveColumn{"b": {Type: "integer"}}, Constraints: map[string]bool{}, Indexes: map[string]bool{}, Triggers: map[string]string{}}
	for _, v := range []string{"app.gone", "app.used", "app.theirs"} {
		live.Views[v] = true
	}
	live.Owners["rel:app.t"], live.Owners["rel:app.gone"], live.Owners["rel:app.used"], live.Owners["rel:app.theirs"] = "app", "app", "app", "platform"
	live.ViewDependents = map[string][]string{"app.used": {"app.theirs"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", ColumnsSet: true, Columns: map[string]*schema.Column{"b": {Type: "integer"}}}}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, Render(p), `drop view "app"."gone";`)
	got := strings.Join(notOwnerWarnings(p), "\n")
	if !strings.Contains(got, "view app.theirs is owned by platform") || !strings.Contains(got, "view app.used") {
		t.Errorf("want the view another role owns named, with the view it keeps:\n%s", got)
	}
	if n := strings.Count(got, "view app.theirs is owned by platform"); n != 1 {
		t.Errorf("one warning per object, got %d:\n%s", n, got)
	}
}

func TestNotOwnerPartitionOfForeignParent(t *testing.T) {
	live := notOwnerLive()
	live.Tables["public.q"].Partitioned = true
	child := &schema.Table{Name: "public.q_2024", PartitionOf: "public.q", Partition: &schema.PartitionSpec{Default: true, Modulus: -1, Remainder: -1}, Comment: "child",
		Indexes: []*schema.Index{{Name: "q_2024_id", Columns: []string{"id"}}}}
	parent := notOwnerTable("public.q", "'01:00:00'::interval")
	parent.PartitionBy = &schema.PartitionBy{Type: "list", Columns: []string{"id"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": parent, "public.q_2024": child}}
	live.OwnerRoles["platform"] = true
	if sql := Render(PlanWith(live, d, Options{})); !strings.Contains(sql, `partition of "public"."q"`) || !strings.Contains(sql, `comment on table "public"."q_2024"`) {
		t.Fatalf("the partition is created with its comment:\n%s", sql)
	}
	delete(live.OwnerRoles, "platform")
	p := PlanWith(live, d, Options{})
	if sql := strings.TrimSpace(Render(p) + RenderConcurrent(p)); sql != "" {
		t.Errorf("a partition of a table the role doesn't own is left out with what the plan does for it:\n%s", sql)
	}
	if got := notOwnerWarnings(p); len(got) != 1 || !strings.HasPrefix(got[0], "table public.q is owned by platform") {
		t.Errorf("want one warning for the parent, got %q", got)
	}
}

func TestNotOwnerMergedWithPruneWarning(t *testing.T) {
	live := notOwnerLive()
	live.Tables["public.q"].Triggers = map[string]string{"old_trigger": "CREATE TRIGGER old_trigger ..."}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.q": notOwnerTable("public.q", "'2 hours'::interval")}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if sql := Render(p); strings.Contains(sql, `"public"."q"`) {
		t.Errorf("nothing for public.q:\n%s", sql)
	}
	var got []string
	for _, w := range notOwnerWarnings(p) {
		if strings.HasPrefix(w, "table public.q ") {
			got = append(got, w)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "the plan leaves out the statement that needs them") || !strings.Contains(got[0], "; for the same reason, --prune does not drop the triggers") {
		t.Errorf("one warning for the table covers both, got %q", got)
	}
}

func TestNotOwnerSequenceAdvanceGoesWithTheRows(t *testing.T) {
	live := notOwnerLive()
	for _, fq := range []string{"public.q", "public.mine"} {
		live.Tables[fq].Columns["id"].Default = "nextval('public.ids'::regclass)"
	}
	live.TableData = map[string]*LiveTableData{"public.q": {Missing: []int{0}}, "public.mine": {Missing: []int{0}}}
	d := &schema.Database{Tables: map[string]*schema.Table{}}
	for _, fq := range []string{"public.q", "public.mine"} {
		dt := notOwnerTable(fq, "'01:00:00'::interval")
		dt.Columns["id"].Default = "nextval('public.ids'::regclass)"
		dt.Comment = "seeded"
		dt.Data = &schema.TableData{Key: []string{"id"}, Rows: []map[string]any{{"id": 5}}}
		d.Tables[fq] = dt
	}
	sql := Render(PlanWith(live, d, Options{}))
	if strings.Contains(sql, `"public"."q"`) {
		t.Errorf("the rows and the sequence advance of a table left out go with it:\n%s", sql)
	}
	for _, want := range []string{`insert into "public"."mine" ("id") values (5);`, `select setval(q.seqrelid, c.top) from (values ('public.ids', (select max("id") from "public"."mine")))`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s in:\n%s", want, sql)
		}
	}
}

func TestStatementObjects(t *testing.T) {
	live := notOwnerLive()
	live.Tables["public.q"].Indexes["q_idx"] = true
	ix := newOwnerIndex(live)
	f := "proc:" + normalizeFunctionSignature("public.f()")
	g := "proc:" + normalizeFunctionSignature("app.g(a integer, b text)")
	for _, tc := range []struct {
		stmt    string
		keys    []string
		need    ownerNeed
		creates []string
	}{
		{`alter table "public"."q" alter column "ttl" set default '2 hours'::interval;`, []string{"rel:public.q"}, needOwner, nil},
		{`alter table if exists only "public"."q" enable row level security;`, []string{"rel:public.q"}, needOwner, nil},
		{`alter table "public"."q" attach partition "public"."q_1" for values in (1);`, []string{"rel:public.q", "rel:public.q_1"}, needOwner, nil},
		{`alter table "public"."c" inherit "public"."q";`, []string{"rel:public.c", "rel:public.q"}, needOwner, nil},
		{`alter table "public"."q" rename to "r";`, []string{"rel:public.q"}, needOwner, []string{"rel:public.r"}},
		{`alter view "public"."pv" set schema "app";`, []string{"rel:public.pv"}, needOwner, []string{"rel:app.pv"}},
		{`alter materialized view "public"."mv" set tablespace "fast";`, []string{"rel:public.mv"}, needOwner, nil},
		{`alter sequence "public"."s" owned by "public"."q"."id";`, []string{"rel:public.s", "rel:public.q"}, needOwner, nil},
		{`alter index "public"."q_idx" set (fillfactor = 70);`, []string{"idx:public.q_idx"}, needOwner, nil},
		{`alter function "public"."f"() stable;`, []string{f}, needOwner, nil},
		{`alter procedure "app"."g"(a integer, b text) security definer;`, []string{g}, needOwner, nil},
		{`alter type "app"."mood" add value 'meh';`, []string{"type:app.mood"}, needOwner, nil},
		{`alter domain "app"."email" set default 'x';`, []string{"type:app.email"}, needOwner, nil},
		{`alter schema "platform" owner to "app";`, []string{"schema:platform"}, needOwner, nil},
		{`alter server "s1" options (set host 'h');`, []string{"srv:s1"}, needOwner, nil},
		{`alter foreign data wrapper "w" options (add x 'y');`, []string{"fdw:w"}, needOwner, nil},
		{`alter publication "p" set table "public"."q", tables in schema "app";`, []string{"pub:p", "rel:public.q"}, needOwner, nil},
		{`alter publication "p" set (publish = 'insert');`, []string{"pub:p"}, needOwner, nil},
		{`alter event trigger "e" disable;`, []string{"evt:e"}, needOwner, nil},
		{`alter extension "hstore" update to '1.8';`, []string{"ext:hstore"}, needOwner, nil},
		{`alter text search dictionary "app"."d" (stopwords = 'english');`, []string{"tsdict:app.d"}, needOwner, nil},
		{`alter statistics "public"."st" set statistics 100;`, []string{"stat:public.st"}, needOwner, nil},
		{`alter policy "p" on "public"."q" using (true);`, []string{"rel:public.q"}, needOwner, nil},
		{`comment on table "public"."q" is 'x';`, []string{"rel:public.q"}, needOwner, nil},
		{`comment on column "public"."q"."ttl" is 'x';`, []string{"rel:public.q"}, needOwner, nil},
		{`comment on constraint "c" on "public"."q" is 'x';`, []string{"rel:public.q"}, needOwner, nil},
		{`comment on constraint "c" on domain "app"."email" is 'x';`, []string{"type:app.email"}, needOwner, nil},
		{`comment on trigger "t" on "public"."q" is 'x';`, []string{"rel:public.q"}, needOwner, nil},
		{`comment on index "public"."q_idx" is 'x';`, []string{"idx:public.q_idx"}, needOwner, nil},
		{`comment on function "public"."f"() is 'x';`, []string{f}, needOwner, nil},
		{`comment on schema "platform" is 'x';`, []string{"schema:platform"}, needOwner, nil},
		{`comment on extension "hstore" is 'x';`, []string{"ext:hstore"}, needOwner, nil},
		{`comment on role "r" is 'x';`, nil, needOwner, nil},
		{`create unique index if not exists "q_ttl" on "public"."q" using btree ("ttl");`, []string{"rel:public.q"}, needOwner, []string{"idx:public.q_ttl"}},
		{`create index concurrently if not exists "q_ttl" on only "public"."q" ("ttl");`, []string{"rel:public.q"}, needOwner, []string{"idx:public.q_ttl"}},
		{`create trigger "t" before update of "on" on "public"."q" for each row execute procedure public.touch();`, []string{"rel:public.q"}, needTrigger, nil},
		{`create policy "p" on "public"."q" for select using (true);`, []string{"rel:public.q"}, needOwner, nil},
		{`create or replace rule "r" as on delete to "public"."q" do instead nothing;`, []string{"rel:public.q"}, needOwner, nil},
		{`create statistics if not exists "public"."st" (ndistinct) on "id", "ttl" from "public"."q";`, []string{"rel:public.q"}, needOwner, []string{"stat:public.st"}},
		{"create or replace function \"public\".\"f\"()\nreturns integer language sql as 'select 2';", nil, needOwner, []string{f}},
		{`create or replace view "public"."pv" as select 1;`, nil, needOwner, []string{"rel:public.pv"}},
		{`create table if not exists "public"."q_1" partition of "public"."q" default;`, []string{"rel:public.q"}, needOwner, []string{"rel:public.q_1"}},
		{`create table if not exists "public"."c" ("id" integer) inherits ("public"."q", "app"."p");`, []string{"rel:public.q", "rel:app.p"}, needOwner, []string{"rel:public.c"}},
		{`create publication "p" for table "public"."q" ("id"), table "app"."t" where (true) with (publish = 'insert');`, []string{"rel:public.q", "rel:app.t"}, needOwner, []string{"pub:p"}},
		{`create schema if not exists "x";`, nil, needOwner, nil},
		{`drop view "app"."a", "app"."b";`, []string{"rel:app.a", "rel:app.b"}, needOwner, nil},
		{`drop index concurrently if exists "public"."q_idx";`, []string{"idx:public.q_idx"}, needOwner, nil},
		{`drop function "public"."f"(), "app"."g"(a integer, b text);`, []string{f, g}, needOwner, nil},
		{`drop trigger "t" on "public"."q";`, []string{"rel:public.q"}, needOwner, nil},
		{`drop user mapping for "app" server "s1";`, []string{"srv:s1"}, needOwner, nil},
		{`grant select, update ("ttl") on table "public"."q" to "reader";`, []string{"rel:public.q"}, needGrant, nil},
		{`revoke grant option for execute on function "public"."f"() from "reader";`, []string{f}, needGrant, nil},
		{`grant usage on foreign server "s1" to "reader";`, []string{"srv:s1"}, needGrant, nil},
		{`grant "platform" to "app";`, nil, needOwner, nil},
		{`alter default privileges in schema "app" grant select on tables to "reader";`, nil, needOwner, nil},
		{`refresh materialized view concurrently "public"."mv";`, []string{"rel:public.mv"}, needOwner, nil},
		{`insert into "public"."q" ("id") values (1);`, []string{"rel:public.q"}, needNothing, nil},
		{`update only "public"."q" set "ttl" = null where "id" = 1;`, []string{"rel:public.q"}, needNothing, nil},
		{`insert into "public"."q" ("id") values (1) on conflict ("id") do nothing;`, []string{"rel:public.q"}, needNothing, nil},
		{dataSequenceStmt("public.q", []dataSequence{{col: "id", owner: "public.q", typed: true}}), []string{"rel:public.q"}, needNothing, nil},
		{dataSequenceStmt("public.q_1", []dataSequence{{col: "id", owner: "public.q", top: 4}}), []string{"rel:public.q"}, needNothing, nil},
		{dataSequenceStmt("public.q", []dataSequence{{col: "id", seq: "public.ids", top: 4}}), nil, needNothing, nil},
		{`do $pgy$begin execute format('alter database %I set "work_mem" = ''8MB''', current_database()); end$pgy$;`, []string{"db:"}, needOwner, nil},
		{`select setval('"public"."s"', 1);`, nil, needOwner, nil},
	} {
		info := statementObjects(tc.stmt, ix)
		var keys []string
		for _, a := range info.acts {
			if a.need != tc.need && (a.need != needOwner || len(tc.creates) == 0) {
				t.Errorf("%s: %s needs %d, want %d", tc.stmt, a.key, a.need, tc.need)
			}
			keys = append(keys, a.key)
		}
		if len(tc.creates) > 0 {
			keys = withoutKeys(keys, tc.creates)
		}
		if !reflect.DeepEqual(keys, tc.keys) && (len(keys) > 0 || len(tc.keys) > 0) {
			t.Errorf("%s: acts on %v, want %v", tc.stmt, keys, tc.keys)
		}
		if !reflect.DeepEqual(info.creates, tc.creates) {
			t.Errorf("%s: creates %v, want %v", tc.stmt, info.creates, tc.creates)
		}
	}
	if info := statementObjects(`grant select, update ("ttl") on table "public"."q" to "reader";`, ix); !reflect.DeepEqual(info.acts[0].privs, []string{"select", "update"}) || info.acts[0].column != "ttl" {
		t.Errorf("grant privileges: %+v", info.acts)
	}
	if info := statementObjects(`alter table "app"."t" alter column "b" type bigint using "b"::bigint;`, ix); !reflect.DeepEqual(info.retyped, []string{"app.t.b"}) {
		t.Errorf("retyped: %v", info.retyped)
	}
	if ix.resolve("idx:public.q_idx") != "rel:public.q" {
		t.Errorf("an index resolves to its table: %s", ix.resolve("idx:public.q_idx"))
	}
	live.Owners["proc:"+normalizeFunctionSignature("public.h(a integer, OUT b text)")] = "platform"
	ix = newOwnerIndex(live)
	want := "proc:" + normalizeFunctionSignature("public.h(a integer, OUT b text)")
	if got := ix.resolve("proc:" + normalizeFunctionSignature("public.h(x integer)")); got != want || ix.owner(got) != "platform" {
		t.Errorf("a routine spelled with other argument names resolves to the live one: %s", got)
	}
}

// withoutKeys returns keys without the first occurrence of each of drop,
// keeping the order of the rest.
func withoutKeys(keys, drop []string) []string {
	out := append([]string(nil), keys...)
	for _, d := range drop {
		for i, k := range out {
			if k == d {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
	}
	return out
}

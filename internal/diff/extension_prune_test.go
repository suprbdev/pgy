package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

var pruneAll = Options{Unsafe: true, Prune: true}

// extensionPruneLive has citext and pg_trgm installed in app, where the YAML
// of extensionPruneDesired declares a table, and pg_stat_statements in a
// schema it declares nothing in.
func extensionPruneLive() *Live {
	l := emptyLive()
	l.Schemas["app"], l.Schemas["monitoring"] = true, true
	l.ExtensionDefs = map[string]*LiveExtension{}
	for name, schemaName := range map[string]string{"citext": "app", "pg_trgm": "app", "pg_stat_statements": "monitoring", "plpgsql": "pg_catalog"} {
		l.Extensions[name] = true
		l.ExtensionDefs[name] = &LiveExtension{Schema: schemaName, Version: "1.0", Relocatable: true}
	}
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	l.Tables["app.keep"] = lt
	return l
}

func extensionPruneDesired(extensions ...string) *schema.Database {
	d := &schema.Database{
		Extensions: []*schema.Extension{},
		Tables:     map[string]*schema.Table{"app.keep": {Name: "keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}},
	}
	for _, name := range extensions {
		d.Extensions = append(d.Extensions, &schema.Extension{Name: name})
	}
	return d
}

func extensionDrops(p *PlanDiff) []string {
	out := []string{}
	for _, s := range p.Drops {
		if strings.HasPrefix(s, "drop extension ") {
			out = append(out, s)
		}
	}
	return out
}

func TestExtensionPruneDropsUndeclared(t *testing.T) {
	p := PlanWith(extensionPruneLive(), extensionPruneDesired("citext"), pruneAll)
	wantStatements(t, p.Drops, []string{`drop extension "pg_trgm";`})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionPruneEmptyBlockIsAuthoritative(t *testing.T) {
	p := PlanWith(extensionPruneLive(), extensionPruneDesired(), pruneAll)
	wantStatements(t, p.Drops, []string{`drop extension "citext", "pg_trgm";`})
}

func TestExtensionPruneNeedsBlockUnsafeAndPrune(t *testing.T) {
	without := extensionPruneDesired()
	without.Extensions = nil
	if got := extensionDrops(PlanWith(extensionPruneLive(), without, pruneAll)); len(got) != 0 {
		t.Errorf("without an extensions block extensions are unmanaged, got %v", got)
	}
	for _, opts := range []Options{{}, {Unsafe: true}, {Prune: true}} {
		if got := extensionDrops(PlanWith(extensionPruneLive(), extensionPruneDesired(), opts)); len(got) != 0 {
			t.Errorf("%+v must not drop extensions, got %v", opts, got)
		}
	}
}

func TestExtensionPruneCandidatesForIntrospection(t *testing.T) {
	live := extensionPruneLive()
	for _, name := range []string{"cube", "earthdistance"} {
		live.Extensions[name] = true
		live.ExtensionDefs[name] = &LiveExtension{Schema: "app", Version: "1.0"}
	}
	live.ExtensionDefs["earthdistance"].Requires = []string{"cube"}
	d := extensionPruneDesired("citext", "earthdistance")
	if got := sortedKeys(prunableExtensions(live, d, pruneAll)); !reflect.DeepEqual(got, []string{"pg_trgm"}) {
		t.Errorf("only pg_trgm may be dropped, got %v", got)
	}
	for _, opts := range []Options{{}, {Unsafe: true}, {Prune: true}} {
		if got := prunableExtensions(live, d, opts); len(got) != 0 {
			t.Errorf("%+v: nothing may be dropped, got %v", opts, got)
		}
	}
}

func TestExtensionPruneSkippedWhenDeclared(t *testing.T) {
	p := PlanWith(extensionPruneLive(), extensionPruneDesired("citext", "pg_trgm"), pruneAll)
	if sql := Render(p); sql != "" {
		t.Errorf("declared extensions must diff clean, got:\n%s", sql)
	}
}

func TestExtensionPruneOnlyInDeclaredSchemas(t *testing.T) {
	p := PlanWith(extensionPruneLive(), extensionPruneDesired(), pruneAll)
	if sql := Render(p); strings.Contains(sql, "pg_stat_statements") {
		t.Errorf("an extension in a schema the YAML declares nothing in must stay:\n%s", sql)
	}
	d := extensionPruneDesired()
	d.Views = map[string]*schema.View{"monitoring.slow": {Name: "slow", Query: "select 1"}}
	wantStatements(t, extensionDrops(PlanWith(extensionPruneLive(), d, pruneAll)), []string{`drop extension "citext", "pg_stat_statements", "pg_trgm";`})
}

func TestExtensionPruneSchemaOfDeclaredExtensionIsNotScope(t *testing.T) {
	live := extensionPruneLive()
	live.ExtensionDefs["citext"].Schema, live.ExtensionDefs["pg_trgm"].Schema = "extensions", "extensions"
	d := extensionPruneDesired()
	d.Extensions = []*schema.Extension{{Name: "citext", Schema: "extensions"}}
	if got := extensionDrops(PlanWith(live, d, pruneAll)); len(got) != 0 {
		t.Errorf("a schema that only holds extensions is not pruned, got %v", got)
	}
}

func TestExtensionPruneKeepExtensions(t *testing.T) {
	opts := Options{Unsafe: true, Prune: true, KeepExtensions: []string{"pg_trgm", "not_installed"}}
	wantStatements(t, extensionDrops(PlanWith(extensionPruneLive(), extensionPruneDesired(), opts)), []string{`drop extension "citext";`})
}

func TestExtensionPruneKeepsPlpgsql(t *testing.T) {
	live := extensionPruneLive()
	live.ExtensionDefs["plpgsql"].Schema = "app"
	wantStatements(t, extensionDrops(PlanWith(live, extensionPruneDesired("citext", "pg_trgm"), pruneAll)), nil)
}

func TestExtensionPruneKeepsRequirementOfKeptExtension(t *testing.T) {
	live := extensionPruneLive()
	for _, name := range []string{"cube", "earthdistance"} {
		live.Extensions[name] = true
		live.ExtensionDefs[name] = &LiveExtension{Schema: "app", Version: "1.0"}
	}
	live.ExtensionDefs["earthdistance"].Requires = []string{"cube"}
	d := extensionPruneDesired("citext", "pg_trgm")
	d.Extensions = append(d.Extensions, &schema.Extension{Name: "earthdistance", Cascade: true})
	if sql := Render(PlanWith(live, d, pruneAll)); sql != "" {
		t.Errorf("cube is required by the declared earthdistance and must stay:\n%s", sql)
	}
	d = extensionPruneDesired("citext", "pg_trgm")
	wantStatements(t, extensionDrops(PlanWith(live, d, pruneAll)), []string{`drop extension "cube", "earthdistance";`})
	wantStatements(t, extensionDrops(PlanWith(live, d, Options{Unsafe: true, Prune: true, KeepExtensions: []string{"earthdistance"}})), nil)
}

func TestExtensionPruneKeepsRequirementOfExtensionBeingCreated(t *testing.T) {
	live := extensionPruneLive()
	live.Extensions["cube"] = true
	live.ExtensionDefs["cube"] = &LiveExtension{Schema: "app", Version: "1.5"}
	d := extensionPruneDesired("citext", "pg_trgm", "earthdistance")
	p := PlanWith(live, d, pruneAll)
	wantStatements(t, extensionCreates(p), []string{`create extension "earthdistance";`})
	wantStatements(t, extensionDrops(p), nil)

	live.ServerVersion = 170000
	live.AvailableExtensions = map[string]*AvailableExtension{
		"a_kcache":      {DefaultVersion: "2.3", Requires: map[string][]string{"2.3": {"pg_trgm"}}},
		"earthdistance": {DefaultVersion: "1.2", Requires: map[string][]string{"1.2": {"cube"}}},
	}
	d = extensionPruneDesired("citext", "earthdistance", "a_kcache")
	wantStatements(t, extensionDrops(PlanWith(live, d, pruneAll)), nil)
}

func TestExtensionPruneKeptWhileInUse(t *testing.T) {
	live := extensionPruneLive()
	live.ExtensionDefs["citext"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "keep", "email"}}, "column email of table app.keep"),
		newExtensionDependent(dependentAddress{kind: "function", names: []string{"app", "norm"}, args: "e citext"}, "function app.norm(citext)"),
		newExtensionDependent(dependentAddress{kind: "rule", names: []string{"app", "v", "_RETURN"}}, "rule _RETURN on view app.v"),
		newExtensionDependent(dependentAddress{kind: "rule", names: []string{"app", "v", "_RETURN"}}, "rule _RETURN on view app.v"),
		newExtensionDependent(dependentAddress{kind: "index", names: []string{"app", "keep_email"}, table: "app.keep"}, "index app.keep_email"),
		newExtensionDependent(dependentAddress{kind: "type", names: []string{"app.email"}, typeName: "app.email"}, "type app.email"),
	}
	live.ExtensionDefs["pg_trgm"].Dependents = []ExtensionDependent{}
	d := extensionPruneDesired()
	d.Tables["app.keep"].Columns["email"] = &schema.Column{Type: "citext"}
	live.Tables["app.keep"].Columns["email"] = &LiveColumn{Type: "citext"}
	p := PlanWith(live, d, pruneAll)
	wantStatements(t, p.Drops, []string{`drop extension "pg_trgm";`})
	if len(p.Warnings) != 1 {
		t.Fatalf("want one warning, got %v", p.Warnings)
	}
	w := p.Warnings[0]
	if w.Kind != "dependents" || !strings.Contains(w.Message, "extension citext is not listed under extensions but still in use (column email of table app.keep, function app.norm(citext), view app.v and 2 more)") {
		t.Errorf("warning: %+v", w)
	}
}

func TestExtensionPruneDroppedAfterItsDependents(t *testing.T) {
	live := extensionPruneLive()
	gone := newLiveTable()
	gone.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.gone"] = gone
	keep := live.Tables["app.keep"]
	keep.Columns["email"] = &LiveColumn{Type: "citext"}
	keep.Indexes["keep_name_trgm"] = true
	keep.IndexDefs["keep_name_trgm"] = &LiveIndex{}
	sig := "app.norm(e citext)"
	live.Functions[sig] = true
	live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Kind: "f", Signature: sig}
	live.Functions["app.other()"] = true
	live.FunctionDefs["app.other()"] = &LiveFunction{Kind: "f", Signature: "app.other()"}
	live.ExtensionDefs["citext"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "keep", "email"}}, "column email of table app.keep"),
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "gone", "email"}}, "column email of table app.gone"),
		newExtensionDependent(dependentAddress{kind: "default value", names: []string{"app", "gone", "email"}}, "default value for column email of table app.gone"),
		newExtensionDependent(dependentAddress{kind: "function", names: []string{"app", "norm"}, args: "e citext"}, "function app.norm(citext)"),
	}
	live.ExtensionDefs["pg_trgm"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "index", names: []string{"app", "keep_name_trgm"}, table: "app.keep"}, "index app.keep_name_trgm"),
	}
	p := PlanWith(live, extensionPruneDesired(), pruneAll)
	wantStatements(t, p.Drops, []string{
		`drop index "app"."keep_name_trgm";`,
		`alter table "app"."keep" drop column "email";`,
		`drop table "app"."gone";`,
		`drop function "app"."norm"(e citext), "app"."other"();`,
		`drop extension "citext", "pg_trgm";`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionPruneDroppedWithPartitionedTable(t *testing.T) {
	live := extensionPruneLive()
	for _, fq := range []string{"app.events", "app.events_2024", "app.events_2024_h1", "app.log", "app.log_2024"} {
		lt := newLiveTable()
		lt.Columns["id"] = &LiveColumn{Type: "integer"}
		lt.Columns["who"] = &LiveColumn{Type: "citext"}
		lt.IsPartition = fq != "app.events" && fq != "app.log"
		lt.Partitioned = !strings.HasSuffix(fq, "_h1") && fq != "app.log_2024"
		live.Tables[fq] = lt
	}
	live.Partitions = map[string][]string{"app.events": {"app.events_2024"}, "app.events_2024": {"app.events_2024_h1"}, "app.log": {"app.log_2024"}}
	live.ExtensionDefs["citext"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "events", "who"}}, "column who of table app.events"),
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "events_2024", "who"}}, "column who of table app.events_2024"),
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "events_2024_h1", "who"}}, "column who of table app.events_2024_h1"),
		newExtensionDependent(dependentAddress{kind: "index", names: []string{"app", "events_2024_h1_who_idx"}, table: "app.events_2024_h1"}, "index app.events_2024_h1_who_idx"),
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "log", "who"}}, "column who of table app.log"),
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "log_2024", "who"}}, "column who of table app.log_2024"),
		newExtensionDependent(dependentAddress{kind: "table constraint", names: []string{"app", "log_2024", "log_who_check"}}, "constraint log_who_check on table app.log_2024"),
		newExtensionDependent(dependentAddress{kind: "trigger", names: []string{"app", "log_2024", "log_touch"}}, "trigger log_touch on table app.log_2024"),
		newExtensionDependent(dependentAddress{kind: "index", names: []string{"app", "log_2024_who_idx"}, table: "app.log_2024", root: "app.log_who_idx"}, "index app.log_2024_who_idx"),
	}
	log := live.Tables["app.log"]
	log.Constraints["log_who_check"], log.ConstraintDefs["log_who_check"] = true, "CHECK ((who <> 'x'::citext))"
	log.Triggers["log_touch"] = ""
	log.Indexes["log_who_idx"], log.IndexDefs["log_who_idx"] = true, &LiveIndex{}
	d := extensionPruneDesired("pg_trgm")
	d.Tables["app.log"] = &schema.Table{Name: "log", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}}
	d.Tables["app.log_2024"] = &schema.Table{Name: "log_2024", PartitionOf: "app.log"}
	p := PlanWith(live, d, pruneAll)
	wantStatements(t, p.Drops, []string{
		`drop trigger "log_touch" on "app"."log";`,
		`alter table "app"."log" drop constraint "log_who_check";`,
		`drop index "app"."log_who_idx";`,
		`alter table "app"."log" drop column "who";`,
		`drop table "app"."events";`,
		`drop extension "citext";`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionPruneKeptExtensionKeepsItsRequirements(t *testing.T) {
	live := extensionPruneLive()
	for _, name := range []string{"cube", "earthdistance"} {
		live.Extensions[name] = true
		live.ExtensionDefs[name] = &LiveExtension{Schema: "app", Version: "1.0", Dependents: []ExtensionDependent{}}
	}
	live.ExtensionDefs["earthdistance"].Requires = []string{"cube"}
	live.ExtensionDefs["earthdistance"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "table column", names: []string{"app", "keep", "pos"}}, "column pos of table app.keep"),
	}
	live.ExtensionDefs["cube"].Dependents = []ExtensionDependent{
		{Description: "type earth", Extension: "earthdistance"},
	}
	live.Tables["app.keep"].Columns["pos"] = &LiveColumn{Type: "earth"}
	d := extensionPruneDesired("citext", "pg_trgm")
	d.Tables["app.keep"].Columns["pos"] = &schema.Column{Type: "earth"}
	p := PlanWith(live, d, pruneAll)
	wantStatements(t, p.Drops, nil)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "extension earthdistance is not listed") {
		t.Errorf("only earthdistance is reported, cube stays because earthdistance does: %v", p.Warnings)
	}
}

func TestExtensionPruneObjectsOfAnotherExtension(t *testing.T) {
	live := extensionPruneLive()
	for _, name := range []string{"base", "addon"} {
		live.Extensions[name] = true
		live.ExtensionDefs[name] = &LiveExtension{Schema: "app", Version: "1.0", Dependents: []ExtensionDependent{}}
	}
	live.ExtensionDefs["base"].Dependents = []ExtensionDependent{{Description: "function addon_fn(base_type)", Extension: "addon"}}
	wantStatements(t, extensionDrops(PlanWith(live, extensionPruneDesired("citext", "pg_trgm", "addon"), pruneAll)), nil)
	wantStatements(t, extensionDrops(PlanWith(live, extensionPruneDesired("citext", "pg_trgm"), pruneAll)), []string{`drop extension "addon", "base";`})
}

func TestExtensionPruneAfterEveryOtherDrop(t *testing.T) {
	live, d := pruneLive(), pruneDesired()
	live.Extensions["hstore"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"hstore": {Schema: "app", Version: "1.8"}}
	live.Servers = map[string]*LiveServer{"old_srv": {Wrapper: "postgres_fdw"}}
	live.EventTriggers = map[string]*LiveEventTrigger{"old_et": {Event: "ddl_command_end", Function: "app.old_et_fn"}}
	live.Publications = map[string]*LivePublication{"old_pub": {}}
	d.Extensions = []*schema.Extension{}
	d.Servers, d.EventTriggers, d.Publications = map[string]*schema.Server{}, map[string]*schema.EventTrigger{}, map[string]*schema.Publication{}
	p := PlanWith(live, d, pruneAll)
	if len(p.Drops) < 5 || p.Drops[len(p.Drops)-1] != `drop extension "hstore";` {
		t.Fatalf("the extension drop must come last: %v", p.Drops)
	}
	for _, other := range []string{`drop server "old_srv";`, `drop event trigger "old_et";`, `drop publication "old_pub";`, `drop table "app"."gone";`} {
		if !strings.Contains(strings.Join(p.Drops[:len(p.Drops)-1], "\n"), other) {
			t.Errorf("missing %s before the extension drop: %v", other, p.Drops)
		}
	}
}

func TestExtensionPruneSimulatedLiveKeepsDependents(t *testing.T) {
	live := extensionPruneLive()
	live.ExtensionDefs["pg_trgm"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "index", names: []string{"other", "t_trgm"}, table: "other.t"}, "index other.t_trgm"),
	}
	d := extensionPruneDesired("citext")
	p := PlanWith(Simulate(live, d), d, pruneAll)
	if len(extensionDrops(p)) != 0 || len(p.Warnings) != 1 {
		t.Errorf("a simulated live database must keep what uses the extension: drops %v, warnings %v", p.Drops, p.Warnings)
	}
}

func TestDroppedObjects(t *testing.T) {
	got := droppedObjects([]string{
		`drop table "app"."a", "app"."b,c";`,
		`drop function "app"."f"(a integer, b text), "app"."g"();`,
		`drop trigger "trg" on "app"."a";`,
		`alter table "app"."a" drop column "c";`,
		`delete from "app"."a" where "id" = 1;`,
	})
	want := map[string]bool{
		`drop table "app"."a"`:                       true,
		`drop table "app"."b,c"`:                     true,
		`drop function "app"."f"(a integer, b text)`: true,
		`drop function "app"."g"()`:                  true,
		`drop trigger "trg" on "app"."a"`:            true,
		`alter table "app"."a" drop column "c"`:      true,
		`delete from "app"."a" where "id" = 1`:       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestNewExtensionDependentRemovedBy(t *testing.T) {
	relation := func(fq string) []string {
		return []string{"drop table " + fq, "drop foreign table " + fq, "drop view " + fq, "drop materialized view " + fq}
	}
	t3 := []string{"app", "t", "c"}
	for _, tc := range []struct {
		address dependentAddress
		want    []string
	}{
		{dependentAddress{kind: "table column", names: t3}, append(relation(`"app"."t"`), `alter table "app"."t" drop column "c"`)},
		{dependentAddress{kind: "default value", names: t3}, append(relation(`"app"."t"`), `alter table "app"."t" drop column "c"`)},
		{dependentAddress{kind: "materialized view column", names: t3}, relation(`"app"."t"`)},
		{dependentAddress{kind: "composite type column", names: t3}, []string{`drop type "app"."t"`}},
		{dependentAddress{kind: "table constraint", names: t3}, append(relation(`"app"."t"`), `alter table "app"."t" drop constraint "c"`)},
		{dependentAddress{kind: "trigger", names: t3}, append(relation(`"app"."t"`), `drop trigger "c" on "app"."t"`)},
		{dependentAddress{kind: "policy", names: t3}, append(relation(`"app"."t"`), `drop policy "c" on "app"."t"`)},
		{dependentAddress{kind: "rule", names: t3}, append(relation(`"app"."t"`), `drop rule "c" on "app"."t"`)},
		{dependentAddress{kind: "rule", names: []string{"app", "v", "_RETURN"}}, relation(`"app"."v"`)},
		{dependentAddress{kind: "index", names: []string{"app", "t_idx"}, table: "app.t"}, append([]string{`drop index "app"."t_idx"`}, relation(`"app"."t"`)...)},
		{dependentAddress{kind: "index", names: []string{"app", "p1_idx"}, table: "app.p1", root: "app.t_idx"}, append([]string{`drop index "app"."p1_idx"`, `drop index "app"."t_idx"`}, relation(`"app"."p1"`)...)},
		{dependentAddress{kind: "statistics object", names: []string{"app", "st"}, table: "app.t"}, append([]string{`drop statistics "app"."st"`}, relation(`"app"."t"`)...)},
		{dependentAddress{kind: "type", names: []string{"app.email"}, typeName: "app.email"}, []string{`drop domain "app"."email"`, `drop type "app"."email"`}},
		{dependentAddress{kind: "domain constraint", names: []string{"app.email"}, typeName: "app.email"}, []string{`drop domain "app"."email"`, `drop type "app"."email"`}},
		{dependentAddress{kind: "function", names: []string{"app", "f"}, args: "e citext"}, []string{`drop function "app"."f"(e citext)`}},
		{dependentAddress{kind: "procedure", names: []string{"app", "p"}}, []string{`drop procedure "app"."p"()`}},
		{dependentAddress{kind: "aggregate", names: []string{"app", "agg"}}, []string{`drop aggregate "app"."agg"(*)`}},
		{dependentAddress{kind: "text search dictionary", names: []string{"app", "d"}}, []string{`drop text search dictionary "app"."d"`}},
		{dependentAddress{kind: "server", names: []string{"srv.1"}}, []string{`drop server "srv.1"`}},
		{dependentAddress{kind: "cast", names: []string{"text"}}, nil},
	} {
		if got := newExtensionDependent(tc.address, "").RemovedBy; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %v:\n got %q\nwant %q", tc.address.kind, tc.address.names, got, tc.want)
		}
	}
	if d := newExtensionDependent(dependentAddress{kind: "rule", names: []string{"app", "v", "_RETURN"}}, "rule _RETURN on view app.v"); d.Description != "view app.v" {
		t.Errorf("a view's rule is described as the view, got %q", d.Description)
	}
}

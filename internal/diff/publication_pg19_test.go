package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func allPublish() []string { return []string{"insert", "update", "delete", "truncate"} }

func TestPublicationExceptCreate(t *testing.T) {
	p := Plan(emptyLive(), pubDB(&schema.Publication{AllTables: true, AllSequences: true,
		Except: []*schema.PublicationTable{{Table: "app.audit"}, {Table: "app.events", Only: true}}}), false)
	want := `create publication "app_pub" for all tables except (table "app"."audit", table only "app"."events"), all sequences;`
	if !findAlter(p, want) {
		t.Errorf("want %q, got %v", want, p.Alters)
	}
	p = Plan(emptyLive(), pubDB(&schema.Publication{AllSequences: true, Publish: []string{"insert"}}), false)
	if want := `create publication "app_pub" for all sequences with (publish = 'insert');`; !findAlter(p, want) {
		t.Errorf("want %q, got %v", want, p.Alters)
	}
}

func TestPublicationExceptSkippedIfExists(t *testing.T) {
	live := livePub(&LivePublication{AllTables: true, AllSequences: true, Publish: allPublish(), Tables: map[string]LivePublicationTable{},
		Except: map[string]bool{"app.audit": false, "app.events": true}})
	live.ServerVersion = 190000
	d := pubDB(&schema.Publication{AllTables: true, AllSequences: true,
		Except: []*schema.PublicationTable{{Table: "app.events", Only: true}, {Table: "app.audit"}}})
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("same publication must not diff, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func TestPublicationExceptAlter(t *testing.T) {
	live := func() *Live {
		l := livePub(&LivePublication{AllTables: true, Publish: allPublish(), Tables: map[string]LivePublicationTable{}, Except: map[string]bool{"app.audit": false}})
		l.ServerVersion = 190000
		return l
	}
	for _, c := range []struct {
		name string
		p    *schema.Publication
		want string
	}{
		{"except added", &schema.Publication{AllTables: true, Except: []*schema.PublicationTable{{Table: "app.audit"}, {Table: "app.log"}}},
			`alter publication "app_pub" set all tables except (table "app"."audit", table "app"."log");`},
		{"except dropped", &schema.Publication{AllTables: true}, `alter publication "app_pub" set all tables;`},
		{"only changed", &schema.Publication{AllTables: true, Except: []*schema.PublicationTable{{Table: "app.audit", Only: true}}},
			`alter publication "app_pub" set all tables except (table only "app"."audit");`},
		{"sequences added", &schema.Publication{AllTables: true, AllSequences: true, Except: []*schema.PublicationTable{{Table: "app.audit"}}},
			`alter publication "app_pub" set all tables except (table "app"."audit"), all sequences;`},
		{"tables dropped", &schema.Publication{AllSequences: true}, `alter publication "app_pub" set all sequences;`},
	} {
		p := Plan(live(), pubDB(c.p), false)
		if len(p.Alters) != 1 || p.Alters[0] != c.want {
			t.Errorf("%s: want %q, got %v", c.name, c.want, p.Alters)
		}
		if sql := Render(Plan(Simulate(live(), pubDB(c.p)), pubDB(c.p), false)); sql != "" {
			t.Errorf("%s: expected no changes after simulate, got:\n%s", c.name, sql)
		}
	}
}

func TestPublicationSwitchToAllTablesInPlace(t *testing.T) {
	live := func(version int) *Live {
		l := livePub(&LivePublication{Publish: []string{"insert"}, Schemas: []string{"billing"},
			Tables: map[string]LivePublicationTable{"app.base": {Only: true}, "app.users": {}}})
		l.ServerVersion = version
		return l
	}
	d := pubDB(&schema.Publication{AllTables: true, Except: []*schema.PublicationTable{{Table: "app.audit"}}})
	p := Plan(live(190000), d, false)
	want := []string{
		`alter publication "app_pub" drop table only "app"."base", "app"."users";`,
		`alter publication "app_pub" drop tables in schema "billing";`,
		`alter publication "app_pub" set all tables except (table "app"."audit");`,
	}
	if strings.Join(p.Alters, "\n") != strings.Join(want, "\n") {
		t.Errorf("PostgreSQL 19 switches in place, want %v, got %v", want, p.Alters)
	}
	sim := Simulate(live(190000), d)
	if lp := sim.Publications["app_pub"]; strings.Join(lp.Publish, ",") != "insert" {
		t.Errorf("an in-place switch keeps the publish option, got %v", lp.Publish)
	}
	if sql := Render(Plan(sim, d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
	if sql := Render(Plan(live(180000), pubDB(&schema.Publication{AllTables: true}), false)); sql != "" {
		t.Errorf("before PostgreSQL 19 the switch needs --unsafe, got:\n%s", sql)
	}
	p = PlanWith(live(180000), pubDB(&schema.Publication{AllTables: true}), Options{Unsafe: true})
	if len(p.Alters) != 2 || p.Alters[0] != `drop publication "app_pub";` || p.Alters[1] != `create publication "app_pub" for all tables;` {
		t.Errorf("before PostgreSQL 19 the switch drops and creates, got %v", p.Alters)
	}
}

func TestPublicationSwitchFromAllNeedsUnsafe(t *testing.T) {
	live := livePub(&LivePublication{AllTables: true, AllSequences: true, Publish: allPublish(), Tables: map[string]LivePublicationTable{}})
	live.ServerVersion = 190000
	d := pubDB(&schema.Publication{Tables: []*schema.PublicationTable{{Table: "app.users"}}})
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("PostgreSQL cannot SET TABLE on a FOR ALL publication; the switch needs --unsafe, got:\n%s", sql)
	}
	p := PlanWith(live, d, Options{Unsafe: true})
	if len(p.Alters) != 2 || p.Alters[0] != `drop publication "app_pub";` || p.Alters[1] != `create publication "app_pub" for table "app"."users";` {
		t.Errorf("expected drop + create, got %v", p.Alters)
	}
}

func TestPublicationEmptyDropsOnlyTables(t *testing.T) {
	live := livePub(&LivePublication{Publish: allPublish(), Tables: map[string]LivePublicationTable{"app.base": {Only: true}}})
	p := Plan(live, pubDB(&schema.Publication{}), false)
	if want := `alter publication "app_pub" drop table only "app"."base";`; len(p.Alters) != 1 || p.Alters[0] != want {
		t.Errorf("want %q, got %v", want, p.Alters)
	}
}

func TestPublicationExceptLeftBeforeAttach(t *testing.T) {
	live := liveWithTable("app.p1", map[string]*LiveColumn{"id": {Type: "integer"}})
	live.ServerVersion = 190000
	live.Tables["app.p"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Partitioned: true}
	live.Publications = map[string]*LivePublication{
		"app_pub": {AllTables: true, Publish: allPublish(), Tables: map[string]LivePublicationTable{}, Except: map[string]bool{"app.p1": false, "app.x": false}},
		"other":   {AllTables: true, Publish: allPublish(), Tables: map[string]LivePublicationTable{}, Except: map[string]bool{"app.p1": false}},
	}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.p":  {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}},
		"app.p1": {Name: "p1", PartitionOf: "app.p", Partition: &schema.PartitionSpec{From: []string{"1"}, To: []string{"200"}, Modulus: -1, Remainder: -1}},
	}, Publications: map[string]*schema.Publication{"app_pub": {Name: "app_pub", AllTables: true, Except: []*schema.PublicationTable{{Table: "app.x"}}}}}
	p := Plan(live, d, false)
	sql := Render(p)
	set, attach := strings.Index(sql, `alter publication "app_pub" set all tables except (table "app"."x");`), strings.Index(sql, `attach partition "app"."p1"`)
	if set < 0 || attach < set || strings.Count(sql, `alter publication "app_pub"`) != 1 {
		t.Errorf("want one SET ALL TABLES before the ATTACH, got:\n%s", sql)
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "publication other excepts app.p1")
	}
	if !warned {
		t.Errorf("an undeclared publication that excepts the table must be reported: %+v", p.Warnings)
	}
}

func TestPublicationPostgres19VersionWarnings(t *testing.T) {
	d := &schema.Database{Publications: map[string]*schema.Publication{
		"rest": {Name: "rest", AllTables: true, Except: []*schema.PublicationTable{{Table: "app.audit"}}},
		"seqs": {Name: "seqs", AllSequences: true},
	}}
	live := emptyLive()
	live.ServerVersion = 180003
	got := []string{}
	for _, w := range Plan(live, d, false).Warnings {
		got = append(got, w.Message)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		"except on publication rest needs PostgreSQL 19, and the server runs PostgreSQL 18",
		"allSequences on publication seqs needs PostgreSQL 19, and the server runs PostgreSQL 18",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	live.ServerVersion = 190000
	for _, w := range Plan(live, d, false).Warnings {
		if strings.Contains(w.Message, "needs PostgreSQL") {
			t.Errorf("PostgreSQL 19 supports them: %s", w.Message)
		}
	}
}

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationPullWarnsAboutViewsReadingExternalRelations pulls views
// over a relation that external.relations lists, as a TimescaleDB
// continuous aggregate reads its materialized hypertable: the view reading
// it is reported, a view reading that view or an application table is not,
// and both are still written.
func TestIntegrationPullWarnsAboutViewsReadingExternalRelations(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(context.Background(), strings.ReplaceAll(`
		create table SCH._runtime_1 (bucket int, total int);
		create table SCH.devices (id int primary key);
		create view SCH.hourly as select bucket, total from SCH._runtime_1;
		create materialized view SCH.daily as select bucket, sum(total) as total from SCH._runtime_1 join SCH.devices on devices.id = bucket group by bucket with no data;
		create view SCH.report as select * from SCH.hourly;
		create view SCH.ids as select id from SCH.devices;`, "SCH", sch)); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, pool), pull.Options{Single: true, Schemas: []string{sch}, External: &schema.External{Relations: []string{sch + "._runtime_*"}}})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"view " + sch + ".hourly reads " + sch + "._runtime_1, an external relation that this YAML leaves out",
		"materialized view " + sch + ".daily reads " + sch + "._runtime_1, an external relation",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}
	if strings.Contains(warnings, sch+".report") || strings.Contains(warnings, sch+".ids") {
		t.Errorf("views that read no external relation are not reported:\n%s", warnings)
	}
	if got := string(res.Files[0].Content); !strings.Contains(got, "view hourly:") || strings.Contains(got, "_runtime_1:") {
		t.Errorf("the view is written, the external relation is not:\n%s", got)
	}
}

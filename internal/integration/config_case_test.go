package integration

import (
	"context"
	"testing"
)

func TestIntegrationSettingValuesKeepTheirCase(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  function cfg():
    returns: text
    language: sql
    set: {timezone: UTC, app.mode: Prod, search_path: "\"MixedCase\", public"}
    body: select 'x'
`)
	var config []string
	if err := pool.QueryRow(context.Background(), `select proconfig from pg_proc where proname = 'cfg' and pronamespace = $1::regnamespace`, sch).Scan(&config); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"TimeZone=UTC": true, "app.mode=Prod": true, `search_path="MixedCase", public`: true}
	for _, c := range config {
		delete(want, c)
	}
	if len(want) > 0 {
		t.Errorf("proconfig %q is missing %v", config, want)
	}
}

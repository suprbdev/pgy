package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestIntegrationRoutineRecreateKeepsUnmanaged(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	user, owner := freshRole(t, pool, "user"), freshRole(t, pool, "owner")
	if _, err := pool.Exec(ctx, strings.NewReplacer("SCH", sch, "USER", user, "OWNER", owner).Replace(`
		create function SCH.ret() returns int language sql strict stable parallel safe cost 5 security definer set search_path = SCH, pg_temp as 'select 1';
		create function SCH.def(a int default 1) returns int language sql immutable leakproof as 'select a';
		create function SCH.ren(a int) returns setof int language sql rows 20 set work_mem = '1MB' as 'select a';
		create procedure SCH.pdef(a int default 1) language sql security definer set statement_timeout = '5s' as 'select 1';
		create procedure SCH.pren(a int) language sql security definer as 'select 1';
		do $$
		declare r record;
		begin
			for r in select p.oid::regprocedure as sig, p.prokind from pg_proc p where p.pronamespace = 'SCH'::regnamespace loop
				execute format('revoke all on %s %s from public', case r.prokind when 'p' then 'procedure' else 'function' end, r.sig);
				execute format('grant execute on %s %s to "USER"', case r.prokind when 'p' then 'procedure' else 'function' end, r.sig);
				execute format('comment on %s %s is %L', case r.prokind when 'p' then 'procedure' else 'function' end, r.sig, 'kept ' || r.sig);
				execute format('alter %s %s owner to "OWNER"', case r.prokind when 'p' then 'procedure' else 'function' end, r.sig);
			end loop;
		end $$;`)); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		var out string
		if err := pool.QueryRow(ctx, `
			select string_agg(format('%s %s %s %s %s %s %s %s %s %s owner=%s acl=%s comment=%s', proname, prosecdef, provolatile, proisstrict, proleakproof, proparallel, procost, prorows, proconfig,
			       pg_get_function_result(oid), proowner::regrole, proacl, obj_description(oid, 'pg_proc')), E'\n' order by proname)
			from pg_proc where pronamespace = $1::regnamespace`, sch).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := state()
	d := loadSpellingYAML(t, strings.ReplaceAll(`
schema SCH:
  function ret():
    returns: bigint
    language: sql
    body: select 1::bigint
  function def(a int):
    returns: int
    language: sql
    body: select a
  function ren(b int):
    returns: setof int
    language: sql
    body: select b
  procedure pdef(a int):
    language: sql
    body: select 1
  procedure pren(b int):
    language: sql
    body: select 1
`, "SCH", sch))
	plan := diff.Plan(introspectAll(t, pool), d, true)
	sql := diff.Render(plan)
	for _, want := range []string{"drop function", "drop procedure"} {
		if strings.Count(sql, want) < 2 {
			t.Fatalf("each routine is dropped and created again; want %s twice in:\n%s", want, sql)
		}
	}
	applyPlan(t, pool, plan)
	if again := diff.Render(diff.Plan(introspectAll(t, pool), d, true)); again != "" {
		t.Errorf("plan did not converge:\n%s", again)
	}
	lines := strings.Split(before, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "ret ") {
			lines[i] = strings.Replace(l, "} integer owner=", "} bigint owner=", 1)
		}
	}
	if got, want := state(), strings.Join(lines, "\n"); got != want {
		t.Errorf("a recreated routine keeps what the YAML leaves unmanaged:\n got:\n%s\nwant:\n%s\nplan:\n%s", got, want, sql)
	}
}

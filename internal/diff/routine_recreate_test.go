package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func recreatedRoutineLive(fnArgs, procArgs string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.CurrentUser = "me"
	live.Owners = map[string]string{}
	fsig, psig := "app.f("+fnArgs+")", "app.p("+procArgs+")"
	fnorm, pnorm := normalizeFunctionSignature(fsig), normalizeFunctionSignature(psig)
	live.Functions[fsig] = true
	live.FunctionDefs[fnorm] = &LiveFunction{Signature: fsig, Args: fnArgs, Kind: "f", Body: "select 1", Returns: "integer", Language: "sql",
		Security: "definer", Volatility: "stable", Strict: true, Parallel: "safe", Cost: 5, Config: map[string]string{"search_path": "app, pg_temp"}}
	live.Procedures[pnorm] = true
	live.ProcedureDefs[pnorm] = &LiveProcedure{Signature: psig, Args: procArgs, Body: "select 1", Language: "sql", Security: "definer", Config: map[string]string{"work_mem": "1MB"}}
	for _, norm := range []string{fnorm, pnorm} {
		live.FunctionGrants[norm] = map[string]map[string]bool{"app_user": {"execute": true}}
		live.FunctionComments[norm] = "unmanaged comment"
		live.Owners["proc:"+norm] = "app_owner"
	}
	return live
}

func recreatedRoutineYAML(fnArgs, procArgs, returns string) *schema.Database {
	return &schema.Database{
		Functions:  map[string]*schema.Function{"app.f" + fnArgs: {Schema: "app", Name: "f", ArgsSig: fnArgs, Returns: returns, Language: "sql", Body: "select 1"}},
		Procedures: map[string]*schema.Procedure{"app.p" + procArgs: {Schema: "app", Name: "p", ArgsSig: procArgs, Language: "sql", Body: "select 1"}},
	}
}

var recreatedRoutineCases = []struct {
	name                      string
	liveFn, liveProc          string
	fnArgs, procArgs, returns string
	fnTarget, procTarget      string
	fnDrop, procDrop          string
}{
	{name: "return type", liveFn: "", liveProc: "", fnArgs: "()", procArgs: "()", returns: "bigint",
		fnTarget: `"app"."f"()`, fnDrop: `drop function "app"."f"();`},
	{name: "removed default", liveFn: "a integer DEFAULT 1", liveProc: "a integer DEFAULT 1", fnArgs: "(a int)", procArgs: "(a int)", returns: "int",
		fnTarget: `"app"."f"(a int)`, procTarget: `"app"."p"(a int)`, fnDrop: `drop function "app"."f"(a int);`, procDrop: `drop procedure "app"."p"(a int);`},
	{name: "renamed arguments", liveFn: "a integer", liveProc: "a integer", fnArgs: "(b int)", procArgs: "(b int)", returns: "int",
		fnTarget: `"app"."f"(b int)`, procTarget: `"app"."p"(b int)`, fnDrop: `drop function "app"."f"(a integer);`, procDrop: `drop procedure "app"."p"(a int);`},
}

func TestRoutineRecreateKeepsUnmanaged(t *testing.T) {
	for _, c := range recreatedRoutineCases {
		live, d := recreatedRoutineLive(c.liveFn, c.liveProc), recreatedRoutineYAML(c.fnArgs, c.procArgs, c.returns)
		sql := Render(Plan(live, d, true))
		want := []string{
			c.fnDrop,
			`create function "app"."f"` + c.fnArgs + ` returns ` + c.returns + ` language sql security definer stable strict parallel safe cost 5 set search_path = app, pg_temp as $$select 1$$;`,
			`revoke execute on function ` + c.fnTarget + ` from public;`,
			`grant execute on function ` + c.fnTarget + ` to "app_user";`,
			`comment on function ` + c.fnTarget + ` is 'unmanaged comment';`,
			`alter function ` + c.fnTarget + ` owner to "app_owner";`,
		}
		if c.procDrop != "" {
			want = append(want,
				c.procDrop,
				`create procedure "app"."p"`+c.procArgs+` language sql security definer set work_mem = '1MB' as $$select 1$$;`,
				`revoke execute on procedure `+c.procTarget+` from public;`,
				`grant execute on procedure `+c.procTarget+` to "app_user";`,
				`comment on procedure `+c.procTarget+` is 'unmanaged comment';`,
				`alter procedure `+c.procTarget+` owner to "app_owner";`,
			)
		}
		for _, w := range want {
			if strings.Count(sql, w) != 1 {
				t.Errorf("%s: want %s once in:\n%s", c.name, w, sql)
			}
		}
		statementOrder(t, sql, c.fnDrop, `create function "app"."f"`, `revoke execute on function `+c.fnTarget, `grant execute on function `+c.fnTarget, `alter function `+c.fnTarget+` owner to`)

		sim := Simulate(live, d)
		if p := Plan(sim, d, true); planSize(p) != 0 {
			t.Errorf("%s: a plan after the simulated one must be empty, got:\n%s", c.name, Render(p))
		}
		norm := normalizeFunctionSignature("app.f" + c.fnArgs)
		if lf := sim.FunctionDefs[norm]; lf == nil || lf.Security != "definer" || lf.Volatility != "stable" || !lf.Strict || lf.Cost != 5 || sim.FunctionComments[norm] != "unmanaged comment" || sim.Owners["proc:"+norm] != "app_owner" || sim.FunctionGrants[norm]["app_user"] == nil {
			t.Errorf("%s: the simulated recreation keeps what the YAML leaves unmanaged: %+v", c.name, lf)
		}
	}
}

func TestRoutineRecreateDeclaredWins(t *testing.T) {
	for _, c := range recreatedRoutineCases {
		live, d := recreatedRoutineLive(c.liveFn, c.liveProc), recreatedRoutineYAML(c.fnArgs, c.procArgs, c.returns)
		f := d.Functions["app.f"+c.fnArgs]
		f.Security, f.Volatility, f.Set, f.Comment, f.Owner = "invoker", "volatile", map[string]string{}, "declared", "me"
		f.Grants = map[string][]string{"reporting": {"execute"}, "public": {}}
		pr := d.Procedures["app.p"+c.procArgs]
		pr.Security, pr.Set, pr.Comment, pr.Owner, pr.RevokePublic = "invoker", map[string]string{}, "declared", "me", true
		pr.Grants = map[string][]string{}
		sql := Render(Plan(live, d, true))
		if !strings.Contains(sql, `create function "app"."f"`+c.fnArgs+` returns `+c.returns+` language sql security invoker volatile strict parallel safe cost 5 as $$select 1$$;`) {
			t.Errorf("%s: declared attributes win, unmanaged ones are kept:\n%s", c.name, sql)
		}
		for _, unwanted := range []string{`to "app_user"`, "unmanaged comment", `owner to "app_owner"`, "work_mem", "search_path"} {
			if strings.Contains(sql, unwanted) {
				t.Errorf("%s: %s is managed by the YAML and not restored:\n%s", c.name, unwanted, sql)
			}
		}
		if !strings.Contains(sql, `grant execute on function `+c.fnTarget+` to "reporting";`) || !strings.Contains(sql, `comment on function `+c.fnTarget+` is 'declared';`) {
			t.Errorf("%s: the declared grants and comment are applied:\n%s", c.name, sql)
		}
		if p := Plan(Simulate(live, d), d, true); planSize(p) != 0 {
			t.Errorf("%s: a plan after the simulated one must be empty, got:\n%s", c.name, Render(p))
		}
	}
}

func TestRoutineRecreateRestoresPublicAndDefaultPrivileges(t *testing.T) {
	live, d := recreatedRoutineLive("", ""), recreatedRoutineYAML("()", "()", "bigint")
	norm := normalizeFunctionSignature("app.f()")
	live.FunctionGrants[norm] = nil
	live.FunctionPublicExec[norm] = true
	live.DefaultACLs = map[string]map[string]map[string]bool{"me|app|functions": {"auditor": {"execute": true}}}
	live.DefaultACLRows = map[string]bool{"me|app|functions": true}
	sql := Render(Plan(live, d, true))
	if !strings.Contains(sql, `revoke execute on function "app"."f"() from "auditor";`) {
		t.Errorf("a grant the default privileges give the new function, which the old one lacked, is revoked:\n%s", sql)
	}
	if strings.Contains(sql, `on function "app"."f"() from public`) || strings.Contains(sql, `on function "app"."f"() to public`) {
		t.Errorf("PUBLIC keeps the EXECUTE it had:\n%s", sql)
	}

	live, d = recreatedRoutineLive("", ""), recreatedRoutineYAML("()", "()", "bigint")
	d.Functions["app.f()"].Grants = map[string][]string{"app_user": {"execute"}}
	sql = Render(Plan(live, d, true))
	if strings.Count(sql, `revoke execute on function "app"."f"() from public;`) != 1 {
		t.Errorf("a grants block that doesn't list PUBLIC leaves its EXECUTE unmanaged, so the revoked one is restored:\n%s", sql)
	}
	if strings.Count(sql, `grant execute on function "app"."f"() to "app_user";`) != 1 {
		t.Errorf("the declared grant is made once:\n%s", sql)
	}
}

func TestRoutineRecreateWithoutUnsafeKeepsLive(t *testing.T) {
	for _, c := range recreatedRoutineCases {
		live, d := recreatedRoutineLive(c.liveFn, c.liveProc), recreatedRoutineYAML(c.fnArgs, c.procArgs, c.returns)
		if sql := Render(Plan(live, d, false)); strings.Contains(sql, "drop ") || strings.Contains(sql, "unmanaged comment") {
			t.Errorf("%s: without --unsafe nothing is dropped or restored:\n%s", c.name, sql)
		}
	}
}

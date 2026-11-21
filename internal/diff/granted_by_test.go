package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// grantedByLive returns a snapshot of table app.t, owned by the
// connecting role pgy (a superuser) on a server of version, where admin
// holds select with the grant option from the owner and nothing else is
// granted.
func grantedByLive(version int) *Live {
	live := grantorLive(version)
	live.TableGrants["app.t"] = map[string]map[string]bool{"admin": {"select*": true}}
	live.Grantors = nil
	return live
}

// grantedByDesired declares table app.t with grants and grantedBy.
func grantedByDesired(grants map[string][]string, by schema.GrantedBy) *schema.Database {
	d := grantorDesired(grants)
	d.Tables["app.t"].GrantedBy = by
	return d
}

// adminGrants is the grants block that gives admin the grant option of
// select, which its grantedBy grants need.
var adminGrants = map[string][]string{"admin": {"select*"}}

func TestGrantedByCreate(t *testing.T) {
	d := grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select"}}})
	stmts, warns := planGrantsWarned(grantedByLive(190000), d, PublicByKind)
	if want := []string{`grant select on table "app"."t" to "reader" granted by "admin";`}; !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("PostgreSQL 19 grants as another role with GRANTED BY:\n got %q, %v\nwant %q", stmts, warns, want)
	}
	for _, version := range []int{150000, 160000, 180000} {
		stmts, warns := planGrantsWarned(grantedByLive(version), d, PublicByKind)
		want := []string{`set role "admin";`, `grant select on table "app"."t" to "reader";`, `reset role;`}
		if !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
			t.Errorf("%d grants as another role under SET ROLE, as GRANTED BY must name the current user:\n got %q, %v\nwant %q", version, stmts, warns, want)
		}
	}
}

func TestGrantedByCreateAfterGrantOption(t *testing.T) {
	live := grantedByLive(190000)
	live.TableGrants["app.t"] = map[string]map[string]bool{}
	d := grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"lead": {"select*"}}, "lead": {"reader": {"select"}}})
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	want := []string{
		`grant select on table "app"."t" to "admin" with grant option;`,
		`grant select on table "app"."t" to "lead" with grant option granted by "admin";`,
		`grant select on table "app"."t" to "reader" granted by "lead";`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grantor's grant option is granted before its grants:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedBySkippedIfLive(t *testing.T) {
	live := grantorLive(190000, "admin")
	for _, grants := range []map[string][]string{adminGrants, nil} {
		stmts, warns := planGrantsWarned(live, grantedByDesired(grants, schema.GrantedBy{"admin": {"reader": {"select"}}}), PublicByKind)
		if len(stmts) != 0 || len(warns) != 0 {
			t.Errorf("a declared grant the object has is left alone and not reported (grants %v):\n%q, %v", grants, stmts, warns)
		}
	}
}

func TestGrantedByRevokesUndeclared(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.TableGrants["app.t"]["writer"] = map[string]bool{"select": true}
	live.Grantors["rel:app.t"]["writer"] = map[string][]string{"select": {"admin"}}
	stmts, _ := planGrantsWarned(live, grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select"}}}), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from "writer" granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grant of a listed grantor that the block doesn't declare is revoked:\n got %q\nwant %q", stmts, want)
	}
	live.ServerVersion = 180000
	stmts, _ = planGrantsWarned(live, grantedByDesired(adminGrants, schema.GrantedBy{"admin": {}}), PublicByKind)
	want := []string{`set role "admin";`, `revoke select on table "app"."t" from "reader";`, `reset role;`, `set role "admin";`, `revoke select on table "app"."t" from "writer";`, `reset role;`}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grantor listed with nothing loses every grant:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByGrantOption(t *testing.T) {
	live := grantorLive(190000, "admin")
	stmts, _ := planGrantsWarned(live, grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select*"}}}), PublicByKind)
	if want := []string{`grant select on table "app"."t" to "reader" with grant option granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a declared grant option is granted:\n got %q\nwant %q", stmts, want)
	}
	live.Grantors["rel:app.t"]["reader"] = map[string][]string{"select*": {"admin"}}
	live.TableGrants["app.t"]["reader"] = map[string]bool{"select*": true}
	stmts, _ = planGrantsWarned(live, grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select"}}}), PublicByKind)
	if want := []string{`revoke grant option for select on table "app"."t" from "reader" granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grant option the block doesn't declare is revoked:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByGrantsBlockInterplay(t *testing.T) {
	live := grantorLive(190000, "", "admin")
	by := schema.GrantedBy{"admin": {"reader": {"select"}}}
	stmts, warns := planGrantsWarned(live, grantedByDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}, by), PublicByKind)
	if len(stmts) != 0 || len(warns) != 0 {
		t.Errorf("the owner's grant belongs to the grants block, the other one to grantedBy:\n%q, %v", stmts, warns)
	}
	stmts, _ = planGrantsWarned(live, grantedByDesired(adminGrants, by), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from "reader";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grants block that leaves reader out revokes only the owner's grant:\n got %q\nwant %q", stmts, want)
	}
	live = grantorLive(190000, "admin", "other")
	stmts, warns = planGrantsWarned(live, grantedByDesired(adminGrants, by), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from "reader" granted by "other";`}; !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("a grant of a grantor grantedBy doesn't list is the grants block's:\n got %q, %v\nwant %q", stmts, warns, want)
	}
	stmts, warns = planGrantsWarned(live, grantedByDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}, by), PublicByKind)
	if len(stmts) != 0 || len(warns) != 1 || !strings.Contains(warns[0].Message, "reader's select is granted by other;") {
		t.Errorf("only the grants of grantors grantedBy doesn't list are reported:\n%q, %v", stmts, warns)
	}
}

func TestGrantedByWithoutGrantsBlock(t *testing.T) {
	live := grantorLive(190000, "")
	stmts, _ := planGrantsWarned(live, grantedByDesired(nil, schema.GrantedBy{"admin": {"writer": {"select"}}}), PublicByKind)
	if want := []string{`grant select on table "app"."t" to "writer" granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("without a grants block only the listed grantors' grants are managed:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByOwnerIsGrants(t *testing.T) {
	stmts, _ := planGrantsWarned(grantedByLive(190000), grantedByDesired(nil, schema.GrantedBy{"pgy": {"admin": {"select*"}, "reader": {"select"}}}), PublicByKind)
	if want := []string{`grant select on table "app"."t" to "reader";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("the owner's entry is a grants block:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByNotOwner(t *testing.T) {
	for _, version := range []int{150000, 190000} {
		live := grantedByLive(version)
		live.RoleDefs["pgy"].Superuser = false
		live.OwnerRoles = map[string]bool{"pgy": true}
		live.SetRoles = map[string]bool{"pgy": true}
		stmts, warns := planGrantsWarned(live, grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select"}}}), PublicByKind)
		if len(stmts) != 0 {
			t.Errorf("%d: a grant the connecting role cannot make as its grantor is left out: %q", version, stmts)
		}
		hint := `have a superuser run grant select on table "app"."t" to "reader" granted by "admin";`
		if version < 190000 {
			hint = `have a superuser run set role "admin"; grant select on table "app"."t" to "reader"; reset role;`
		}
		if len(warns) != 1 || warns[0].Kind != "not_owner" || !strings.Contains(warns[0].Message, "grantedBy declares admin's grant of select to reader") || !strings.Contains(warns[0].Message, hint) {
			t.Errorf("%d: want one not_owner warning that gives the statement, %q: %v", version, hint, warns)
		}
	}
}

func TestGrantedByMember(t *testing.T) {
	live := grantedByLive(190000)
	live.RoleDefs["pgy"].Superuser = false
	live.OwnerRoles = map[string]bool{"pgy": true, "admin": true}
	live.SetRoles = map[string]bool{"pgy": true}
	d := grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select"}}})
	stmts, warns := planGrantsWarned(live, d, PublicByKind)
	if want := []string{`grant select on table "app"."t" to "reader" granted by "admin";`}; !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("a role with the grantor's privileges grants GRANTED BY it on 19:\n got %q, %v\nwant %q", stmts, warns, want)
	}
	live.ServerVersion = 160000
	live.SetRoles["admin"] = true
	stmts, _ = planGrantsWarned(live, d, PublicByKind)
	if want := []string{`set role "admin";`, `grant select on table "app"."t" to "reader";`, `reset role;`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a role that may SET ROLE to the grantor grants as it before 19:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByConnectingRole(t *testing.T) {
	live := grantedByLive(150000)
	live.Owners["rel:app.t"] = "owner"
	live.RoleDefs["pgy"].Superuser = false
	live.TableGrants["app.t"]["pgy"] = map[string]bool{"select*": true}
	d := grantedByDesired(nil, schema.GrantedBy{"pgy": {"reader": {"select"}}})
	stmts, warns := planGrantsWarned(live, d, PublicByKind)
	if want := []string{`grant select on table "app"."t" to "reader";`}; !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("the connecting role, neither the owner nor a superuser, grants as itself with a plain GRANT:\n got %q, %v\nwant %q", stmts, warns, want)
	}
	live.RoleDefs["pgy"].Superuser = true
	stmts, warns = planGrantsWarned(live, d, PublicByKind)
	if len(stmts) != 0 || len(warns) != 1 || !strings.Contains(warns[0].Message, "is a superuser, whose GRANT records the owner as the grantor") {
		t.Errorf("a superuser cannot be the grantor before 19:\n%q, %v", stmts, warns)
	}
}

func TestGrantedByColumn(t *testing.T) {
	live := grantorLive(180000)
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.t": {"a": {"reader": {"update": true}}}}
	live.ColumnGrantors = map[string]map[string]LiveGrantors{"app.t": {"a": {"reader": {"update": {"admin"}}}}}
	d := grantorDesired(map[string][]string{"admin": {"select*", "update*"}, "reader": {"select"}})
	d.Tables["app.t"].Columns["a"].GrantedBy = schema.GrantedBy{"admin": {"reader": {"insert"}}}
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	want := []string{
		`set role "admin";`, `revoke update ("a") on table "app"."t" from "reader";`, `reset role;`,
		`grant update on table "app"."t" to "admin" with grant option;`,
		`set role "admin";`, `grant insert ("a") on table "app"."t" to "reader";`, `reset role;`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a column's grantedBy block manages its grantors' column grants:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantedByRoutineTypeSchema(t *testing.T) {
	live := grantedByLive(190000)
	live.Functions["app.f()"] = true
	live.FunctionDefs = map[string]*LiveFunction{"app.f()": {Signature: "app.f()", Kind: "f", Body: "select 1", Returns: "integer", Language: "sql"}}
	live.FunctionGrants = map[string]map[string]map[string]bool{"app.f()": {"admin": {"execute*": true}}}
	live.Types["app.e"] = true
	live.TypeGrants = map[string]map[string]map[string]bool{"app.e": {"admin": {"usage*": true}}}
	live.SchemaGrants = map[string]map[string]map[string]bool{"app": {"admin": {"usage*": true}}}
	d := &schema.Database{
		Functions:       map[string]*schema.Function{"app.f()": {Schema: "app", Name: "f", Grants: map[string][]string{"admin": {"execute*"}}, GrantedBy: schema.GrantedBy{"admin": {"reader": {"execute"}}}}},
		Types:           map[string]*schema.TypeDef{"app.e": {Schema: "app", Name: "e", GrantedBy: schema.GrantedBy{"admin": {"reader": {"usage"}}}}},
		SchemaGrantedBy: map[string]schema.GrantedBy{"app": {"admin": {"reader": {"usage"}}}},
	}
	stmts, _ := planGrantsWarned(live, d, PublicExplicit)
	for _, want := range []string{
		`grant usage on schema "app" to "reader" granted by "admin";`,
		`grant usage on type "app"."e" to "reader" granted by "admin";`,
		`grant execute on function "app"."f"() to "reader" granted by "admin";`,
	} {
		if !strings.Contains(strings.Join(stmts, "\n"), want) {
			t.Errorf("want %q in:\n%s", want, strings.Join(stmts, "\n"))
		}
	}
}

func TestGrantedByAllExpanded(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.TableGrants["app.t"]["admin"] = map[string]bool{"select*": true, "insert*": true}
	live.Grantors["rel:app.t"]["admin"] = map[string][]string{"select*": {""}, "insert*": {""}}
	d := expandAllPrivileges(grantedByDesired(map[string][]string{"admin": {"select*", "insert*"}}, schema.GrantedBy{"admin": {"reader": {"all"}}}), 190000)
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	if !strings.Contains(strings.Join(stmts, "\n"), `grant delete, insert, maintain, references, trigger, truncate, update on table "app"."t" to "reader" granted by "admin";`) {
		t.Errorf("ALL in a grantedBy block stands for the privileges of the kind:\n%s", strings.Join(stmts, "\n"))
	}
}

func TestGrantedBySimulateConverges(t *testing.T) {
	for _, version := range []int{180000, 190000} {
		live := grantorLive(version, "admin")
		live.TableGrants["app.t"]["writer"] = map[string]bool{"select": true}
		live.Grantors["rel:app.t"]["writer"] = map[string][]string{"select": {"admin"}}
		for _, d := range []*schema.Database{
			grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"lead": {"select*"}}, "lead": {"other": {"select"}}}),
			grantedByDesired(nil, schema.GrantedBy{"admin": {"lead": {"select"}}}),
		} {
			if stmts, _ := planGrantsWarned(live, d, PublicByKind); len(stmts) == 0 {
				t.Fatalf("%d: want a plan", version)
			}
			if stmts, warns := planGrantsWarned(Simulate(live, d), d, PublicByKind); len(stmts) != 0 || len(warns) != 0 {
				t.Errorf("%d: the simulated result of a plan needs nothing more:\n%q, %v", version, stmts, warns)
			}
		}
	}
}

func TestGrantedByPlan(t *testing.T) {
	plan := Plan(grantorLive(190000, "admin"), grantedByDesired(adminGrants, schema.GrantedBy{"admin": {"reader": {"select", "insert"}}}), false)
	if !findAlter(plan, `grant insert on table "app"."t" to "reader" granted by "admin";`) {
		t.Errorf("the plan grants as the grantor:\n%s", strings.Join(plan.Alters, "\n"))
	}
}

func TestGrantedByTableRevokeRegrantsColumn(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.TableGrants["app.t"]["reader"] = map[string]bool{"select": true, "update": true}
	live.Grantors["rel:app.t"]["reader"] = map[string][]string{"select": {"admin"}, "update": {"admin"}}
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.t": {"a": {"reader": {"update": true}}}}
	live.ColumnGrantors = map[string]map[string]LiveGrantors{"app.t": {"a": {"reader": {"update": {"admin"}}}}}
	d := grantedByDesired(map[string][]string{"admin": {"select*", "update*"}}, schema.GrantedBy{"admin": {"reader": {"select"}}})
	d.Tables["app.t"].Columns["a"].GrantedBy = schema.GrantedBy{"admin": {"reader": {"update"}}}
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	want := []string{
		`revoke update on table "app"."t" from "reader" granted by "admin";`,
		`grant update on table "app"."t" to "admin" with grant option;`,
		`grant update ("a") on table "app"."t" to "reader" granted by "admin";`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a table REVOKE takes the grantor's column grants too, which are given back:\n got %q\nwant %q", stmts, want)
	}
}

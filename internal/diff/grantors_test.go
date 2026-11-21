package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// grantorLive returns a snapshot of table app.t, owned by the connecting
// role pgy (a superuser) on a server of version, where reader holds
// select, granted by the roles in grantors ("" for the owner), and admin
// holds select with the grant option from the owner.
func grantorLive(version int, grantors ...string) *Live {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.CurrentUser = "pgy"
	live.ServerVersion = version
	live.Schemas["app"] = true
	live.RoleDefs = map[string]*LiveRole{"pgy": {Superuser: true}, "admin": {}, "reader": {}}
	live.Owners = map[string]string{"rel:app.t": "pgy"}
	live.OwnerRoles = map[string]bool{"pgy": true, "admin": true, "reader": true}
	live.SetRoles = map[string]bool{"pgy": true, "admin": true, "reader": true}
	live.TableGrants = map[string]map[string]map[string]bool{"app.t": {"reader": {"select": true}, "admin": {"select*": true}}}
	live.Grantors = map[string]LiveGrantors{"rel:app.t": {"reader": {"select": grantors}, "admin": {"select*": {""}}}}
	return live
}

// grantorDesired declares table app.t with grants.
func grantorDesired(grants map[string][]string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Grants: grants, Columns: map[string]*schema.Column{"a": {Type: "integer"}}}}}
}

func TestGrantorRevokeGrantedBy(t *testing.T) {
	stmts, warns := planGrantsWarned(grantorLive(190000, "admin"), grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
	want := []string{`revoke select on table "app"."t" from "reader" granted by "admin";`}
	if !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("PostgreSQL 19 revokes another role's grant GRANTED BY it:\n got %q, %v\nwant %q", stmts, warns, want)
	}
}

func TestGrantorRevokeSetRole(t *testing.T) {
	for _, version := range []int{150000, 180000} {
		stmts, warns := planGrantsWarned(grantorLive(version, "admin"), grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
		want := []string{`set role "admin";`, `revoke select on table "app"."t" from "reader";`, `reset role;`}
		if !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
			t.Errorf("%d revokes another role's grant as that role:\n got %q, %v\nwant %q", version, stmts, warns, want)
		}
	}
}

func TestGrantorRevokeOwnerAndOthers(t *testing.T) {
	stmts, _ := planGrantsWarned(grantorLive(190000, "", "admin"), grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
	want := []string{`revoke select on table "app"."t" from "reader" granted by "admin";`, `revoke select on table "app"."t" from "reader";`}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("each grantor's grant is revoked, the deeper grantor first:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeWithoutGrantorsIsPlain(t *testing.T) {
	live := grantorLive(190000)
	live.Grantors = nil
	stmts, _ := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from "reader";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("an object holding only its owner's grants gets a plain REVOKE:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeLeftOut(t *testing.T) {
	for _, version := range []int{150000, 190000} {
		live := grantorLive(version, "admin")
		live.RoleDefs["pgy"].Superuser = false
		live.OwnerRoles = map[string]bool{"pgy": true}
		live.SetRoles = map[string]bool{"pgy": true}
		stmts, warns := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
		if len(stmts) != 0 {
			t.Errorf("%d: a grant the connecting role cannot revoke is left out: %q", version, stmts)
		}
		if len(warns) != 1 || warns[0].Kind != "not_owner" || !strings.Contains(warns[0].Message, `admin granted select to reader`) {
			t.Errorf("%d: want one not_owner warning naming the grantor: %v", version, warns)
		}
		hint := `revoke select on table "app"."t" from "reader" granted by "admin";`
		if version < 190000 {
			hint = `set role "admin"; revoke select on table "app"."t" from "reader"; reset role;`
		}
		if len(warns) == 1 && !strings.Contains(warns[0].Message, hint) {
			t.Errorf("%d: the warning gives the statement to run, %q: %s", version, hint, warns[0].Message)
		}
	}
}

func TestGrantorRevokeMember(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.RoleDefs["pgy"].Superuser = false
	live.OwnerRoles = map[string]bool{"pgy": true, "admin": true}
	stmts, warns := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from "reader" granted by "admin";`}; !reflect.DeepEqual(stmts, want) || len(warns) != 0 {
		t.Errorf("a role with the grantor's privileges revokes GRANTED BY it:\n got %q, %v\nwant %q", stmts, warns, want)
	}
	live.ServerVersion = 160000
	live.SetRoles = map[string]bool{"pgy": true, "admin": true}
	stmts, _ = planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}}), PublicByKind)
	if want := []string{`set role "admin";`, `revoke select on table "app"."t" from "reader";`, `reset role;`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a role that may SET ROLE to the grantor revokes as it:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeGrantOption(t *testing.T) {
	live := grantorLive(190000)
	live.TableGrants["app.t"]["reader"] = map[string]bool{"select*": true}
	live.Grantors["rel:app.t"]["reader"] = map[string][]string{"select*": {"admin"}, "select": {""}}
	stmts, _ := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}), PublicByKind)
	if want := []string{`revoke grant option for select on table "app"."t" from "reader" granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grant option is revoked from the grants that give it:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeOrder(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.TableGrants["app.t"]["owner2"] = map[string]bool{"select": true}
	live.Grantors["rel:app.t"]["owner2"] = map[string][]string{"select": {"reader"}}
	live.TableGrants["app.t"]["reader"] = map[string]bool{"select*": true}
	live.Grantors["rel:app.t"]["reader"] = map[string][]string{"select*": {"admin"}}
	stmts, _ := planGrantsWarned(live, grantorDesired(map[string][]string{}), PublicByKind)
	want := []string{
		`revoke select on table "app"."t" from "owner2" granted by "reader";`,
		`revoke select on table "app"."t" from "reader" granted by "admin" cascade;`,
		`revoke select on table "app"."t" from "admin" cascade;`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("grants that depend on a grant option are revoked ahead of it:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeCascadeRegrants(t *testing.T) {
	live := grantorLive(190000, "admin")
	live.TableGrants["app.t"]["owner2"] = map[string]bool{"select*": true}
	live.Grantors["rel:app.t"]["owner2"] = map[string][]string{"select*": {"admin"}}
	live.TableGrants["app.t"]["other"] = map[string]bool{"select": true}
	live.Grantors["rel:app.t"]["other"] = map[string][]string{"select": {"owner2"}}
	stmts, warns := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select"}, "reader": {"select"}, "other": {"select"}, "owner2": {"select"}}), PublicByKind)
	want := []string{
		`revoke grant option for select on table "app"."t" from "owner2" granted by "admin" cascade;`,
		`revoke grant option for select on table "app"."t" from "admin" cascade;`,
		`grant select on table "app"."t" to "other";`,
		`grant select on table "app"."t" to "owner2";`,
		`grant select on table "app"."t" to "reader";`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grant option that kept grants depend on is revoked with CASCADE and the kept grants given back:\n got %q\nwant %q", stmts, want)
	}
	if len(warns) != 0 {
		t.Errorf("the grants given back are the owner's: %v", warns)
	}

	live = grantorLive(190000, "", "admin")
	stmts, _ = planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select"}, "reader": {"select"}}), PublicByKind)
	if want := []string{`revoke grant option for select on table "app"."t" from "admin" cascade;`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grantee that the owner's grant still gives the privilege keeps it:\n got %q\nwant %q", stmts, want)
	}

	live = grantorLive(190000, "admin")
	stmts, _ = planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select"}}), PublicExplicit)
	if want := []string{`revoke select on table "app"."t" from "reader" granted by "admin";`, `revoke grant option for select on table "app"."t" from "admin" cascade;`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("a grant the block revokes is not given back:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokeColumn(t *testing.T) {
	live := grantorLive(150000)
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.t": {"a": {"reader": {"update": true}}}}
	live.ColumnGrantors = map[string]map[string]LiveGrantors{"app.t": {"a": {"reader": {"update": {"admin"}}}}}
	d := grantorDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}})
	d.Tables["app.t"].Columns["a"].Grants = map[string][]string{}
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	want := []string{`set role "admin";`, `revoke update ("a") on table "app"."t" from "reader";`, `reset role;`}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("column grants are revoked by their grantor too:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorRevokePublicWarns(t *testing.T) {
	live := grantorLive(190000)
	live.TableGrants["app.t"]["public"] = map[string]bool{"select": true}
	live.Grantors["rel:app.t"]["public"] = map[string][]string{"select": {"admin"}}
	stmts, warns := planGrantsWarned(live, grantorDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}), PublicByKind)
	if want := []string{`revoke select on table "app"."t" from public granted by "admin";`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("PUBLIC's grant from another role is revoked GRANTED BY it:\n got %q\nwant %q", stmts, want)
	}
	if len(warns) != 1 || warns[0].Kind != "public_revoke" {
		t.Errorf("the PUBLIC revoke is warned about: %v", warns)
	}
}

func TestGrantorSetRoleRevokesFirst(t *testing.T) {
	live := grantorLive(150000, "admin")
	live.SchemaGrants = map[string]map[string]map[string]bool{"app": {"admin": {"usage": true}}}
	d := grantorDesired(map[string][]string{})
	d.SchemaGrants = map[string]map[string][]string{"app": {}}
	stmts, _ := planGrantsWarned(live, d, PublicByKind)
	want := []string{
		`set role "admin";`, `revoke select on table "app"."t" from "reader";`, `reset role;`,
		`revoke usage on schema "app" from "admin";`,
		`revoke select on table "app"."t" from "admin" cascade;`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Errorf("a revoke run as its grantor goes before the revokes that take the grantor's privileges:\n got %q\nwant %q", stmts, want)
	}
}

func TestGrantorKeptGrantReported(t *testing.T) {
	stmts, warns := planGrantsWarned(grantorLive(190000, "admin"), grantorDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}), PublicByKind)
	if len(stmts) != 0 {
		t.Errorf("a granted privilege the block lists is kept: %q", stmts)
	}
	if len(warns) != 1 || warns[0].Kind != "" || !strings.Contains(warns[0].Message, `table "app"."t": reader's select is granted by admin`) {
		t.Errorf("want a warning that the grant's grantor is not reproduced: %v", warns)
	}
	if _, warns := planGrantsWarned(grantorLive(190000, ""), grantorDesired(map[string][]string{"admin": {"select*"}, "reader": {"select"}}), PublicByKind); len(warns) != 0 {
		t.Errorf("the owner's grants are not reported: %v", warns)
	}
}

func TestForeignGrants(t *testing.T) {
	live := grantorLive(190000, "", "admin", "pgy")
	live.ColumnGrantors = map[string]map[string]LiveGrantors{"app.t": {"a": {"reader": {"update*": {"admin"}}}}}
	got := live.ForeignGrants()
	want := []ForeignGrant{
		{Object: "table app.t", Schema: "app", Key: "rel:app.t", Grantee: "reader", Privilege: "select", Grantor: "admin"},
		{Object: "table app.t", Schema: "app", Key: "rel:app.t", Column: "a", Grantee: "reader", Privilege: "update*", Grantor: "admin"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ForeignGrants leaves out the owner's and the connecting role's grants:\n got %+v\nwant %+v", got, want)
	}
}

func TestGrantorRenamedTableKeepsGrantors(t *testing.T) {
	live := grantorLive(190000, "admin")
	d := grantorDesired(nil)
	d.Tables["app.u"] = d.Tables["app.t"]
	delete(d.Tables, "app.t")
	d.Tables["app.u"].Name, d.Tables["app.u"].RenamedFrom = "u", "app.t"
	d.Tables["app.u"].Grants = map[string][]string{"admin": {"select*"}}
	plan := Plan(live, d, false)
	if !findAlter(plan, `revoke select on table "app"."u" from "reader" granted by "admin";`) {
		t.Errorf("a renamed table's grants keep their grantors:\n%s", strings.Join(plan.Alters, "\n"))
	}
}

package diff

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// initPrivsQ reads pg_init_privs for the objects extensions created: one row
// per privilege the extension's script left with a role other than the
// object's owner, then or now, and one with an empty grantee for an initial
// ACL that holds nothing else. The first column is the object's
// Live.ExtensionMembers key, with the signature of a routine not yet
// normalized. ALTER ... OWNER TO leaves pg_init_privs alone, so the entry
// of the role that owned the object when the script ran, the one it
// granted to itself, stays there.
const initPrivsQ = `
    select case ip.classoid
             when 'pg_proc'::regclass then 'proc:' || pn.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
             when 'pg_class'::regclass then 'rel:' || cn.nspname || '.' || c.relname
             when 'pg_type'::regclass then 'type:' || tn.nspname || '.' || t.typname
             when 'pg_language'::regclass then 'language:' || l.lanname
             when 'pg_foreign_data_wrapper'::regclass then 'fdw:' || w.fdwname
             when 'pg_namespace'::regclass then 'schema:' || s.nspname
           end,
           case when a.grantee = 0 then 'public' else coalesce(r.rolname::text, '') end,
           coalesce(lower(a.privilege_type) || case when a.is_grantable then '*' else '' end, '')
    from pg_init_privs ip
    left join pg_proc p on ip.classoid = 'pg_proc'::regclass and p.oid = ip.objoid
    left join pg_namespace pn on pn.oid = p.pronamespace
    left join pg_class c on ip.classoid = 'pg_class'::regclass and c.oid = ip.objoid
    left join pg_namespace cn on cn.oid = c.relnamespace
    left join pg_type t on ip.classoid = 'pg_type'::regclass and t.oid = ip.objoid
    left join pg_namespace tn on tn.oid = t.typnamespace
    left join pg_language l on ip.classoid = 'pg_language'::regclass and l.oid = ip.objoid
    left join pg_foreign_data_wrapper w on ip.classoid = 'pg_foreign_data_wrapper'::regclass and w.oid = ip.objoid
    left join pg_namespace s on ip.classoid = 'pg_namespace'::regclass and s.oid = ip.objoid
    left join lateral aclexplode(ip.initprivs) a on a.grantee <> coalesce(p.proowner, c.relowner, t.typowner, l.lanowner, w.fdwowner, s.nspowner) and a.grantee <> a.grantor
    left join pg_roles r on r.oid = a.grantee
    where ip.privtype = 'e' and ip.objsubid = 0
    and coalesce(p.oid, c.oid, t.oid, l.oid, w.oid, s.oid) is not null
`

// userObjects returns the condition that keeps the catalog rows of the
// objects introspection reads, those outside the system schemas, with nsp
// the alias of their pg_namespace row, plus the extension members in
// pg_catalog, where extensions whose control file fixes the schema (plv8,
// plpgsql itself) put their routines and types: oid is the object's OID and
// class its catalog. Built-in objects have no pg_depend rows, so only the
// rows of pg_catalog pay for the check.
func userObjects(nsp, class, oid string) string {
	return fmt.Sprintf(`(%[1]s.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and %[1]s.nspname !~ '^pg_(toast_)?temp_'
            or %[1]s.nspname = 'pg_catalog' and exists (select from pg_depend ext_m where ext_m.classid = '%[2]s'::regclass and ext_m.objid = %[3]s and ext_m.deptype = 'e'))`, nsp, class, oid)
}

// extensionConfigQ lists the configuration tables of extensions
// (pg_extension_config_dump) with the condition that selects the rows
// pg_dump keeps of them, those an application added.
const extensionConfigQ = `
    select n.nspname || '.' || c.relname, coalesce(u.condition, '')
    from pg_extension x
    cross join lateral unnest(x.extconfig, x.extcondition) u(rel, condition)
    join pg_class c on c.oid = u.rel and c.relkind in ('r', 'p')
    join pg_namespace n on n.oid = c.relnamespace
`

func (l *Live) scanInitPrivs(rows pgx.Rows) error {
	for rows.Next() {
		var key, grantee, priv string
		if err := rows.Scan(&key, &grantee, &priv); err != nil {
			return err
		}
		if sig, ok := strings.CutPrefix(key, "proc:"); ok {
			key = "proc:" + publicScope.functionSignature(sig)
		}
		if l.InitPrivs[key] == nil {
			l.InitPrivs[key] = map[string]map[string]bool{}
		}
		if grantee != "" && priv != "" {
			addLiveGrant(l.InitPrivs, key, grantee, priv)
		}
	}
	return nil
}

// InitialGrants returns the privileges the extension member key held right
// after CREATE EXTENSION, as role -> privilege set without the owner: its
// pg_init_privs entry, or, when the extension's script changed nothing,
// publicDefault for PUBLIC (execute for routines, usage for types and
// languages, "" for kinds that give PUBLIC nothing). key is a
// Live.ExtensionMembers key; ok is false for an object no extension owns,
// whose privileges have no baseline other than the default of its kind.
func (l *Live) InitialGrants(key, publicDefault string) (grants map[string]map[string]bool, ok bool) {
	if l == nil || !l.ExtensionMembers[key] {
		return nil, false
	}
	if g, recorded := l.InitPrivs[key]; recorded {
		return g, true
	}
	if publicDefault == "" {
		return nil, true
	}
	return map[string]map[string]bool{"public": {publicDefault: true}}, true
}

// GrantChanges returns the grants block that turns the ACL initial into
// current: every grantee whose privileges differ between the two, with its
// current privileges (none for a grantee that lost them all). It is nil
// when the two are the same.
func GrantChanges(current, initial map[string]map[string]bool) map[string]map[string]bool {
	var out map[string]map[string]bool
	set := func(role string) {
		if out == nil {
			out = map[string]map[string]bool{}
		}
		out[role] = cloneSet(current[role])
	}
	for role, privs := range current {
		if !sameSet(privs, initial[role]) {
			set(role)
		}
	}
	for role, privs := range initial {
		if len(privs) > 0 && len(current[role]) == 0 {
			set(role)
		}
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	n := 0
	for k, ok := range a {
		if !ok {
			continue
		}
		if !b[k] {
			return false
		}
		n++
	}
	for _, ok := range b {
		if ok {
			n--
		}
	}
	return n == 0
}

// WithPublicDefault returns liveG with PUBLIC holding priv when present
// reports that it still has PostgreSQL's default (EXECUTE on routines, USAGE
// on types) and the ACL doesn't spell PUBLIC out.
func WithPublicDefault(liveG map[string]map[string]bool, priv string, present bool) map[string]map[string]bool {
	return withPublicDefault(liveG, priv, present)
}

// withoutInitialGrants returns liveG, the ACL of an extension member, as a
// grants block has to see it: without the privileges of initial (see
// InitialGrants) held by grantees that desired doesn't name. What the
// extension's script granted is then left alone until the block names the
// grantee, which makes that grantee's privileges exact. With coversPublic,
// PUBLIC is compared in full even when it is not named.
func withoutInitialGrants(liveG, initial map[string]map[string]bool, desired map[string][]string, coversPublic bool) map[string]map[string]bool {
	if len(initial) == 0 {
		return liveG
	}
	out := make(map[string]map[string]bool, len(liveG))
	for role, privs := range liveG {
		_, named := desired[role]
		if named || len(initial[role]) == 0 || (role == "public" && coversPublic) {
			out[role] = privs
			continue
		}
		granted := map[string]bool{}
		for p := range initial[role] {
			granted[strings.TrimSuffix(p, "*")] = true
		}
		for p := range privs {
			if granted[strings.TrimSuffix(p, "*")] {
				continue
			}
			if out[role] == nil {
				out[role] = map[string]bool{}
			}
			out[role][p] = true
		}
	}
	return out
}

// memberLiveGrants returns liveG, the ACL of the object with
// Live.ExtensionMembers key key, as a grants block desired has to see it
// under mode: unchanged for an object no extension owns, and otherwise
// without what its extension granted (see withoutInitialGrants).
// publicDefault is as for InitialGrants.
func memberLiveGrants(live *Live, mode PublicGrants, key, publicDefault string, liveG map[string]map[string]bool, desired map[string][]string) map[string]map[string]bool {
	initial, ok := live.InitialGrants(key, publicDefault)
	if !ok {
		return liveG
	}
	return withoutInitialGrants(liveG, initial, desired, mode == PublicAuthoritative)
}

// coversPublic reports whether a grants block covers PUBLIC under mode, so
// that PUBLIC's privileges it doesn't list are revoked. byKind is whether
// PostgreSQL gives PUBLIC nothing by default on that kind of object.
func coversPublic(mode PublicGrants, byKind bool, grants map[string][]string) bool {
	_, listed := grants["public"]
	switch mode {
	case PublicAuthoritative:
		return true
	case PublicExplicit:
		return listed
	}
	return byKind || listed
}

// publicRevokeWarnings returns a warning for each statement of revokes that
// takes privileges from PUBLIC, for a grants block that covers PUBLIC
// without listing it. byKind says why blocks on that kind of object do.
func publicRevokeWarnings(revokes []string, mode PublicGrants, byKind string) []Warning {
	var out []Warning
	for _, s := range revokes {
		if !strings.HasSuffix(s, " from public;") {
			continue
		}
		why := byKind
		if mode == PublicAuthoritative {
			why = "policy.public_grants: authoritative makes every grants block cover PUBLIC"
		}
		out = append(out, Warning{Kind: "public_revoke", Message: fmt.Sprintf("%s: the grants block doesn't list public, so this revokes PUBLIC's privileges (%s; list public to keep them)", strings.TrimSuffix(s, ";"), why)})
	}
	return out
}

// pendingGrants stands in for the unknown ACL of an object that an
// extension of the same plan is about to create, starting from liveG, the
// default privileges it is created with. Each grantee the block lists
// without privileges is taken to hold some, so that it is revoked. The
// PUBLIC default of the object's kind is not part of liveG: the extension
// may revoke it, so what the block lists for PUBLIC has to be granted.
func pendingGrants(liveG map[string]map[string]bool, desired map[string][]string) map[string]map[string]bool {
	out := cloneACL(liveG)
	for role, privs := range desired {
		if len(privs) == 0 {
			out[role] = map[string]bool{"all": true}
		}
	}
	return out
}

// membersPending reports whether live lacks objects that exist once the
// plan's extension statements have run: stmts creates or updates an
// extension, or an earlier step of the same plan created one.
func membersPending(live *Live, stmts []string) bool {
	for _, s := range stmts {
		if strings.HasPrefix(s, "create extension ") || strings.HasPrefix(s, "alter extension ") && strings.Contains(s, " update to ") {
			return true
		}
	}
	for _, le := range live.ExtensionDefs {
		if le != nil && le.Created {
			return true
		}
	}
	return false
}

// resolveAttributeOnly lines up the declarations of desired that manage an
// object pgy doesn't create (a function or procedure without a body, a view
// without a query, a type without a kind, a table without columns, an
// aggregate without sfunc and stype, a text search dictionary without a
// template) with the live objects they are about.
// A routine declared in another spelling than the live one (argument types
// only, other argument names) is rewritten to the live signature, so that
// everything compared by signature finds it. A declaration whose object
// doesn't exist is emptied, with a warning, unless pending says an
// extension of this plan may still create it. A table declared with columns
// that an extension owns is cut down to what an attribute-only declaration
// manages, with a warning. desired itself is not modified.
func resolveAttributeOnly(live *Live, desired *schema.Database, pending bool) (*schema.Database, []Warning) {
	if live == nil || desired == nil {
		return desired, nil
	}
	out := *desired
	var warns []Warning
	warn := func(format string, args ...any) {
		warns = append(warns, Warning{Message: fmt.Sprintf(format, args...)})
	}
	missing := func(kind, name, lacks string) {
		article := "a"
		if strings.ContainsRune("aeiou", rune(kind[0])) {
			article = "an"
		}
		warn("%s %s has no %s, so it only manages %s %s that already exists (one an extension owns, for example), but the database has no such %s and this plan creates or updates no extension; nothing is planned for it", kind, name, lacks, article, kind, kind)
	}

	liveFuncs := normalizedLiveFunctions(live)
	byIdentity, declared := map[string]string{}, map[string]bool{}
	for norm, lf := range live.FunctionDefs {
		if lf != nil && !lf.Internal {
			byIdentity[routineIdentity(lf.Signature, false)] = norm
		}
	}
	for k, f := range desired.Functions {
		if f != nil {
			declared[normalizeFunctionSignature(schema.RoutineName(k)+f.ArgsSig)] = true
		}
	}
	functions := cloneMap(desired.Functions, nil)
	for _, k := range sortedKeys(desired.Functions) {
		f := desired.Functions[k]
		if f == nil || f.HasBody() {
			continue
		}
		name := schema.RoutineName(k)
		if norm := normalizeFunctionSignature(name + f.ArgsSig); liveFuncs[norm] || live.FunctionDefs[norm] != nil {
			continue
		}
		lnorm, found := byIdentity[routineIdentity(name+f.ArgsSig, false)]
		switch {
		case found && declared[lnorm]:
			warn("function %s%s is the function declared as %s; the declaration is ignored", name, f.ArgsSig, live.FunctionDefs[lnorm].Signature)
			delete(functions, k)
		case found:
			c := *f
			c.ArgsSig = signatureArgs(live.FunctionDefs[lnorm].Signature)
			delete(functions, k)
			functions[schema.FunctionKey(name, c.ArgsSig)] = &c
			declared[lnorm] = true
		case !pending:
			missing("function", name+f.ArgsSig, "body")
			functions[k] = &schema.Function{Schema: f.Schema, Name: f.Name, ArgsSig: f.ArgsSig}
		}
	}
	if desired.Functions != nil {
		out.Functions = functions
	}

	byIdentity, declared = map[string]string{}, map[string]bool{}
	for norm, lp := range live.ProcedureDefs {
		if lp != nil {
			byIdentity[routineIdentity(lp.Signature, true)] = norm
		}
	}
	for k, p := range desired.Procedures {
		if p != nil {
			declared[normalizeFunctionSignature(schema.RoutineName(k)+p.ArgsSig)] = true
		}
	}
	procedures := cloneMap(desired.Procedures, nil)
	for _, k := range sortedKeys(desired.Procedures) {
		p := desired.Procedures[k]
		if p == nil || p.HasBody() {
			continue
		}
		name := schema.RoutineName(k)
		if live.Procedures[normalizeFunctionSignature(name+p.ArgsSig)] {
			continue
		}
		lnorm, found := byIdentity[routineIdentity(name+p.ArgsSig, true)]
		switch {
		case found && declared[lnorm]:
			warn("procedure %s%s is the procedure declared as %s; the declaration is ignored", name, p.ArgsSig, live.ProcedureDefs[lnorm].Signature)
			delete(procedures, k)
		case found:
			c := *p
			c.ArgsSig = signatureArgs(live.ProcedureDefs[lnorm].Signature)
			delete(procedures, k)
			procedures[schema.FunctionKey(name, c.ArgsSig)] = &c
			declared[lnorm] = true
		case !pending:
			missing("procedure", name+p.ArgsSig, "body")
			procedures[k] = &schema.Procedure{Schema: p.Schema, Name: p.Name, ArgsSig: p.ArgsSig}
		}
	}
	if desired.Procedures != nil {
		out.Procedures = procedures
	}

	views := cloneMap(desired.Views, nil)
	for _, k := range sortedKeys(desired.Views) {
		vw := desired.Views[k]
		if vw == nil || strings.TrimSpace(vw.Query) != "" {
			continue
		}
		kind, other, exists, mismatch := "view", "materialized view", live.Views[k], live.MatViews[k]
		if vw.Materialized {
			kind, other, exists, mismatch = other, kind, mismatch, exists
		}
		switch {
		case exists || pending && !mismatch:
			continue
		case mismatch:
			warn("%s %s is a %s in the database and has to be declared as one; nothing is planned for it", kind, k, other)
		default:
			missing(kind, k, "query")
		}
		views[k] = &schema.View{Schema: vw.Schema, Name: vw.Name, Materialized: vw.Materialized}
	}
	if desired.Views != nil {
		out.Views = views
	}

	types := cloneMap(desired.Types, nil)
	for _, k := range sortedKeys(desired.Types) {
		td := desired.Types[k]
		if td == nil || td.Kind != "" || pending || liveType(live, k) {
			continue
		}
		missing("type", k, "type: key")
		types[k] = &schema.TypeDef{Name: td.Name, Schema: td.Schema}
	}
	if desired.Types != nil {
		out.Types = types
	}

	tables := cloneMap(desired.Tables, nil)
	for _, k := range sortedKeys(desired.Tables) {
		dt := desired.Tables[k]
		switch {
		case dt == nil:
		case !dt.AttributeOnly() && live.Tables[k] != nil && live.ExtensionMembers["rel:"+k]:
			warn("table %s belongs to extension %s, which defines its columns, keys and indexes, so pgy manages only its privileges, owner, comment, row level security, policies and rows; declare it without columns", k, live.MemberExtension["rel:"+k])
			tables[k] = memberTable(dt)
		case !dt.AttributeOnly() || live.Tables[k] != nil || pending:
		default:
			missing("table", k, "columns")
			tables[k] = &schema.Table{Name: dt.Name}
		}
	}
	if desired.Tables != nil {
		out.Tables = tables
	}

	aggregates := cloneMap(desired.Aggregates, nil)
	for _, k := range sortedKeys(desired.Aggregates) {
		ag := desired.Aggregates[k]
		if ag == nil || !ag.AttributeOnly() || pending {
			continue
		}
		name := schema.RoutineName(k)
		if live.AggregateDefs[normalizeFunctionSignature(name+aggregateArgs(ag.ArgsSig))] != nil {
			continue
		}
		missing("aggregate", name+ag.ArgsSig, "sfunc and stype")
		aggregates[k] = &schema.Aggregate{Schema: ag.Schema, Name: ag.Name, ArgsSig: ag.ArgsSig}
	}
	if desired.Aggregates != nil {
		out.Aggregates = aggregates
	}

	dictionaries := cloneMap(desired.TSDictionaries, nil)
	for _, k := range sortedKeys(desired.TSDictionaries) {
		td := desired.TSDictionaries[k]
		if td == nil || !td.AttributeOnly() || pending || live.TSDictionaries[k] {
			continue
		}
		missing("text search dictionary", k, "template")
		dictionaries[k] = &schema.TSDictionary{Schema: td.Schema, Name: td.Name}
	}
	if desired.TSDictionaries != nil {
		out.TSDictionaries = dictionaries
	}
	return &out, warns
}

// memberTable returns the part of dt that applies to a table an extension
// owns: everything an attribute-only declaration manages (see
// schema.Table.AttributeOnly).
func memberTable(dt *schema.Table) *schema.Table {
	out := &schema.Table{Name: dt.Name, Grants: dt.Grants, Owner: dt.Owner, Comment: dt.Comment, DependsOn: dt.DependsOn,
		RowLevelSecurity: dt.RowLevelSecurity, ForceRowLevelSecurity: dt.ForceRowLevelSecurity, Policies: dt.Policies, Data: dt.Data}
	if dt.Foreign != nil {
		out.Foreign = &schema.ForeignTable{}
	}
	if out.Data != nil && len(out.Data.Key) == 0 {
		out.Data = &schema.TableData{Key: dt.DataKey(), Rows: dt.Data.Rows}
	}
	return out
}

// attributeOnly reports whether desired's declaration of kind (type,
// function, procedure, aggregate, table, view or tsdictionary) with key k
// manages an object that already exists instead of one pgy creates (see
// resolveAttributeOnly). Such a declaration doesn't make pgy create its
// schema: the extension that creates the object may create the schema too.
func attributeOnly(desired *schema.Database, kind, k string) bool {
	switch kind {
	case "type":
		td := desired.Types[k]
		return td != nil && td.Kind == ""
	case "function":
		f := desired.Functions[k]
		return f != nil && !f.HasBody()
	case "procedure":
		p := desired.Procedures[k]
		return p != nil && !p.HasBody()
	case "aggregate":
		ag := desired.Aggregates[k]
		return ag != nil && ag.AttributeOnly()
	case "table":
		dt := desired.Tables[k]
		return dt != nil && dt.AttributeOnly()
	case "view":
		vw := desired.Views[k]
		return vw != nil && strings.TrimSpace(vw.Query) == ""
	case "tsdictionary":
		td := desired.TSDictionaries[k]
		return td != nil && td.AttributeOnly()
	}
	return false
}

// signatureArgs returns the parenthesized argument list of a routine
// signature ("schema.name(args)").
func signatureArgs(sig string) string {
	if i := strings.IndexByte(sig, '('); i >= 0 {
		return sig[i:]
	}
	return "()"
}

// liveType reports whether a type or domain named fq exists in live,
// whatever its kind.
func liveType(live *Live, fq string) bool {
	return live.Types[fq] || live.BaseTypes[fq] || live.Domains[fq]
}

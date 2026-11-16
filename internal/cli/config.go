package cli

import (
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "slices"
    "strings"

    yaml "gopkg.in/yaml.v3"

    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/validate"
)

type FileConfig struct {
    DSN           string   `yaml:"dsn"`
    SchemaRoot    string   `yaml:"schema_root"`
    Schemas       []string `yaml:"schemas"`
    MigrationsDir string   `yaml:"migrations_dir"`
    MigrationsTable string `yaml:"migrations_table"`
    BufferPath    string   `yaml:"buffer"`
    Quiet         *bool    `yaml:"quiet"`
    Verbose       *bool    `yaml:"verbose"`
    JSON          *bool    `yaml:"json"`
    CI            *CIConfig `yaml:"ci"`
    Policy        *PolicyConfig `yaml:"policy"`
}

// PolicyConfig is the policy: block: schema rules whose findings warn, and
// fail under --strict when ci.fail_on includes policy.
type PolicyConfig struct {
    SecurityDefiner  string   `yaml:"security_definer"`
    RequireGrants    []string `yaml:"require_grants"`
    RequireOwners    any      `yaml:"require_owners"`
    RowLevelSecurity []string `yaml:"row_level_security"`
    ConcurrentIndexes bool    `yaml:"concurrent_indexes"`
    PublicGrants     string   `yaml:"public_grants"`
    LockTimeout      string   `yaml:"lock_timeout"`
    StatementTimeout string   `yaml:"statement_timeout"`
}

// migratePolicy holds the policy: settings that aren't schema rules: how
// pgy diff treats PUBLIC and indexes, and pgy migrate's default timeouts.
type migratePolicy struct {
    ConcurrentIndexes bool
    PublicGrants      diff.PublicGrants
    LockTimeout, StatementTimeout string
}

var durationRe = regexp.MustCompile(`^\d+(\.\d+)?\s*(us|ms|s|min|h|d)?$`)

// resolveMigratePolicy checks the policy: block's non-rule settings.
func resolveMigratePolicy(pc *PolicyConfig) (migratePolicy, error) {
    mp := migratePolicy{}
    if pc == nil { return mp, nil }
    mp.ConcurrentIndexes = pc.ConcurrentIndexes
    switch pc.PublicGrants {
    case "", "by_kind":
    case "authoritative":
        mp.PublicGrants = diff.PublicAuthoritative
    case "explicit":
        mp.PublicGrants = diff.PublicExplicit
    default:
        return mp, fmt.Errorf("policy.public_grants: unknown value %q (use by_kind, authoritative or explicit)", pc.PublicGrants)
    }
    for _, t := range []struct{ key, v string; dst *string }{{"lock_timeout", pc.LockTimeout, &mp.LockTimeout}, {"statement_timeout", pc.StatementTimeout, &mp.StatementTimeout}} {
        if t.v == "" { continue }
        if !durationRe.MatchString(strings.TrimSpace(t.v)) { return mp, fmt.Errorf("policy.%s: %q is not a duration such as 5s or 500ms", t.key, t.v) }
        *t.dst = strings.TrimSpace(t.v)
    }
    return mp, nil
}

// resolvePolicy checks a policy: block and returns the rules it enables.
// require_owners takes a list of kinds, or true for all of them.
func resolvePolicy(pc *PolicyConfig) (validate.Policy, error) {
    p := validate.Policy{}
    if pc == nil { return p, nil }
    switch pc.SecurityDefiner {
    case "":
    case "hardened":
        p.SecurityDefinerHardened = true
    default:
        return p, fmt.Errorf("policy.security_definer: unknown value %q (use hardened)", pc.SecurityDefiner)
    }
    kinds := func(key string, list []string) (map[string]bool, error) {
        out := map[string]bool{}
        for _, k := range list {
            if !slices.Contains(validate.PolicyKinds, k) {
                return nil, fmt.Errorf("policy.%s: unknown kind %q (use %s)", key, k, strings.Join(validate.PolicyKinds, ", "))
            }
            out[k] = true
        }
        return out, nil
    }
    var err error
    if p.RequireGrants, err = kinds("require_grants", pc.RequireGrants); err != nil { return p, err }
    switch v := pc.RequireOwners.(type) {
    case nil:
    case bool:
        if v { p.RequireOwners, _ = kinds("require_owners", validate.PolicyKinds) }
    case []any:
        list := []string{}
        for _, e := range v { list = append(list, fmt.Sprint(e)) }
        if p.RequireOwners, err = kinds("require_owners", list); err != nil { return p, err }
    default:
        return p, fmt.Errorf("policy.require_owners: want true or a list of kinds")
    }
    p.RowLevelSecurity = map[string]bool{}
    for _, s := range pc.RowLevelSecurity { p.RowLevelSecurity[s] = true }
    return p, nil
}

// CIConfig is the ci: block, which sets what --strict fails on. Keys left
// out keep the built-in preset's values.
type CIConfig struct {
    FailOn       []string `yaml:"fail_on"`
    ForbidUnsafe *bool    `yaml:"forbid_unsafe"`
}

// strictKinds are the finding kinds ci.fail_on accepts: lock, public_revoke
// and dependents warnings from pgy diff, policy: rule findings, validate
// findings, the objects pgy pull can't express, and all of them.
var strictKinds = map[string]bool{"lock": true, "public_revoke": true, "dependents": true, "policy": true, "validate": true, "unsupported": true, "all": true}

// defaultFailOn is what --strict fails on without a ci.fail_on.
var defaultFailOn = []string{"lock", "public_revoke", "policy", "validate"}

// strictSettings resolves --strict's fail_on set and forbid_unsafe from the
// ci: block over the built-in preset.
func strictSettings(ci *CIConfig) (map[string]bool, bool, error) {
    failOn, forbid := defaultFailOn, true
    if ci != nil {
        if ci.FailOn != nil { failOn = ci.FailOn }
        if ci.ForbidUnsafe != nil { forbid = *ci.ForbidUnsafe }
    }
    out := map[string]bool{}
    for _, k := range failOn {
        if !strictKinds[k] { return nil, false, fmt.Errorf("ci.fail_on: unknown kind %q (use lock, public_revoke, dependents, policy, validate, unsupported or all)", k) }
        out[k] = true
    }
    return out, forbid, nil
}

// configCandidates is the default lookup order when --config is not given.
// The dot-prefixed names are legacy and kept for backwards compatibility.
var configCandidates = []string{"pgy.yaml", "pgy.yml", ".pgy.yaml", ".pgy.yml"}

// loadFileConfig loads the config file. If explicit is non-empty it must
// exist; otherwise the first existing candidate in cwd is used. The returned
// path is the file that was read ("" if none).
func loadFileConfig(cwd, explicit string) (*FileConfig, string, error) {
    var path string
    if explicit != "" {
        path = explicit
    } else {
        for _, name := range configCandidates {
            p := filepath.Join(cwd, name)
            if _, err := os.Stat(p); err == nil {
                path = p
                break
            }
        }
        if path == "" {
            return &FileConfig{}, "", nil
        }
    }
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, "", fmt.Errorf("reading config %s: %w", path, err)
    }
    var fc FileConfig
    if err := yaml.Unmarshal(b, &fc); err != nil {
        return nil, "", fmt.Errorf("parsing config %s: %w", path, err)
    }
    return &fc, path, nil
}

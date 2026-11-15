package cli

import (
    "fmt"
    "os"
    "path/filepath"

    yaml "gopkg.in/yaml.v3"
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
}

// CIConfig is the ci: block, which sets what --strict fails on. Keys left
// out keep the built-in preset's values.
type CIConfig struct {
    FailOn       []string `yaml:"fail_on"`
    ForbidUnsafe *bool    `yaml:"forbid_unsafe"`
}

// strictKinds are the finding kinds ci.fail_on accepts: lock, public_revoke
// and dependents warnings from pgy diff, validate findings, the objects pgy
// pull can't express, and all of them.
var strictKinds = map[string]bool{"lock": true, "public_revoke": true, "dependents": true, "validate": true, "unsupported": true, "all": true}

// defaultFailOn is what --strict fails on without a ci.fail_on.
var defaultFailOn = []string{"lock", "public_revoke", "validate"}

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
        if !strictKinds[k] { return nil, false, fmt.Errorf("ci.fail_on: unknown kind %q (use lock, public_revoke, dependents, validate, unsupported or all)", k) }
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

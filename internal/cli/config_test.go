package cli

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func writeFile(t *testing.T, path, content string) {
    t.Helper()
    if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
        t.Fatal(err)
    }
}

func TestConfigDefaultNames(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "dsn: from-yaml\n")

    fc, path, err := loadFileConfig(dir, "")
    if err != nil {
        t.Fatal(err)
    }
    if fc.DSN != "from-yaml" {
        t.Fatalf("expected dsn from pgy.yaml, got %q", fc.DSN)
    }
    if filepath.Base(path) != "pgy.yaml" {
        t.Fatalf("expected pgy.yaml, got %q", path)
    }
}

func TestConfigPrefersNewNameOverLegacy(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yml"), "dsn: new\n")
    writeFile(t, filepath.Join(dir, ".pgy.yml"), "dsn: legacy\n")

    fc, path, err := loadFileConfig(dir, "")
    if err != nil {
        t.Fatal(err)
    }
    if fc.DSN != "new" {
        t.Fatalf("expected new-style config to win, got %q from %q", fc.DSN, path)
    }
}

func TestConfigLegacyFallback(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, ".pgy.yml"), "dsn: legacy\n")

    fc, path, err := loadFileConfig(dir, "")
    if err != nil {
        t.Fatal(err)
    }
    if fc.DSN != "legacy" {
        t.Fatalf("expected legacy config, got %q", fc.DSN)
    }
    if filepath.Base(path) != ".pgy.yml" {
        t.Fatalf("expected .pgy.yml, got %q", path)
    }
}

func TestConfigNoneFound(t *testing.T) {
    fc, path, err := loadFileConfig(t.TempDir(), "")
    if err != nil {
        t.Fatal(err)
    }
    if path != "" || fc.DSN != "" {
        t.Fatalf("expected empty config, got path=%q dsn=%q", path, fc.DSN)
    }
}

func TestConfigExplicitPath(t *testing.T) {
    dir := t.TempDir()
    custom := filepath.Join(dir, "custom.yaml")
    writeFile(t, custom, "dsn: custom\n")
    // A default-named file must be ignored when --config is given.
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "dsn: default\n")

    fc, path, err := loadFileConfig(dir, custom)
    if err != nil {
        t.Fatal(err)
    }
    if fc.DSN != "custom" || path != custom {
        t.Fatalf("expected explicit config, got dsn=%q path=%q", fc.DSN, path)
    }
}

func TestConfigExplicitPathMissing(t *testing.T) {
    _, _, err := loadFileConfig(t.TempDir(), filepath.Join(t.TempDir(), "nope.yaml"))
    if err == nil {
        t.Fatal("expected error for missing explicit config")
    }
}

func TestConfigInvalidYAML(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "dsn: [unclosed\n")

    _, _, err := loadFileConfig(dir, "")
    if err == nil {
        t.Fatal("expected parse error")
    }
}

func TestConfigPruneKeepExtensions(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "prune:\n  keep_extensions: [pg_stat_statements, \" postgis \"]\n")
    fc, _, err := loadFileConfig(dir, "")
    if err != nil {
        t.Fatal(err)
    }
    keep, err := resolvePrune(fc.Prune)
    if err != nil {
        t.Fatal(err)
    }
    if strings.Join(keep, ",") != "pg_stat_statements,postgis" {
        t.Errorf("keep_extensions = %v", keep)
    }
    if keep, err := resolvePrune(nil); err != nil || len(keep) != 0 {
        t.Errorf("no prune block: %v, %v", keep, err)
    }
    if _, err := resolvePrune(&PruneConfig{KeepExtensions: []string{"citext", " "}}); err == nil || !strings.Contains(err.Error(), "prune.keep_extensions") {
        t.Errorf("an empty name must be rejected, got %v", err)
    }
}

func TestConfigPolicyStrictExpressions(t *testing.T) {
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "policy:\n  strict_expressions: true\n")
    fc, _, err := loadFileConfig(dir, "")
    if err != nil {
        t.Fatal(err)
    }
    if mp, err := resolveMigratePolicy(fc.Policy); err != nil || !mp.StrictExpressions {
        t.Errorf("policy.strict_expressions: want true, got %+v, %v", mp, err)
    }
    if mp, _ := resolveMigratePolicy(&PolicyConfig{}); mp.StrictExpressions {
        t.Error("strict_expressions is off by default")
    }
}

func TestDiscoverSchemasSkipsConfigAndDotfiles(t *testing.T) {
    dir := t.TempDir()
    for _, f := range []string{"a.yml", "sub/b.yaml", ".golangci.yml", "sub/.pgy.yml", "sub/pgy.yaml", "custom.yml", ".github/workflows/ci.yml", "node_modules/x/y.yml", "notes.txt"} {
        p := filepath.Join(dir, filepath.FromSlash(f))
        if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { t.Fatal(err) }
        if err := os.WriteFile(p, nil, 0o644); err != nil { t.Fatal(err) }
    }
    got := strings.Join(discoverSchemas(dir, filepath.Join(dir, "custom.yml")), ",")
    if want := strings.Join([]string{"a.yml", filepath.Join("sub", "b.yaml")}, ","); got != want {
        t.Errorf("discoverSchemas = %s, want %s", got, want)
    }
}

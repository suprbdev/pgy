package cli

import (
    "os"
    "path/filepath"
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

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
    BufferPath    string   `yaml:"buffer"`
    Quiet         *bool    `yaml:"quiet"`
    Verbose       *bool    `yaml:"verbose"`
    JSON          *bool    `yaml:"json"`
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

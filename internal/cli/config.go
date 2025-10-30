package cli

import (
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

func loadFileConfig(cwd string) (*FileConfig, error) {
    path := filepath.Join(cwd, ".pgy.yml")
    b, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) { return &FileConfig{}, nil }
        return nil, err
    }
    var fc FileConfig
    if err := yaml.Unmarshal(b, &fc); err != nil { return nil, err }
    return &fc, nil
}



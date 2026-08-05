package cli

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "runtime/debug"
    "strings"

    "github.com/spf13/cobra"
)

type GlobalConfig struct {
    ConfigPath    string
    DSN           string
    SchemaRoot    string
    Schemas       []string
    MigrationsDir string
    BufferPath    string
    Quiet         bool
    Verbose       bool
    JSON          bool
}

func AttachGlobalFlags(root *cobra.Command) {
    // env helpers
    getenv := func(key, def string) string {
        if v := os.Getenv(key); v != "" {
            return v
        }
        return def
    }

    var cfg GlobalConfig
    root.PersistentFlags().StringVar(&cfg.ConfigPath, "config", getenv("PGY_CONFIG", ""), "Config file path (default: pgy.yaml, pgy.yml, .pgy.yaml, .pgy.yml)")
    root.PersistentFlags().StringVar(&cfg.DSN, "dsn", getenv("PGY_DSN", ""), "PostgreSQL DSN")
    root.PersistentFlags().StringVar(&cfg.SchemaRoot, "schema-root", getenv("PGY_SCHEMA_ROOT", "."), "Schema root directory")
    root.PersistentFlags().StringSliceVar(&cfg.Schemas, "schemas", splitCSV(getenv("PGY_SCHEMAS", "")), "Comma-separated YAML schema files (relative to schema-root)")
    root.PersistentFlags().StringVar(&cfg.MigrationsDir, "migrations-dir", getenv("PGY_MIGRATIONS_DIR", "./migrations"), "Migrations directory")
    root.PersistentFlags().StringVar(&cfg.BufferPath, "buffer", getenv("PGY_BUFFER", "./.pgy.buffer.sql"), "Buffer SQL file path")
    root.PersistentFlags().BoolVar(&cfg.Quiet, "quiet", os.Getenv("PGY_QUIET") == "1", "Suppress non-essential output")
    root.PersistentFlags().BoolVar(&cfg.Verbose, "verbose", os.Getenv("PGY_VERBOSE") == "1", "Verbose output")
    root.PersistentFlags().BoolVar(&cfg.JSON, "json", os.Getenv("PGY_JSON") == "1", "JSON output where applicable")

    // store on context for subcommands
    root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
        // Commands that need no schema/DB config: skip config load and the
        // schema-root walk entirely.
        switch cmd.Name() {
        case "version", "help":
            return nil
        }

        // Load config file and merge with precedence: flags > env > config > defaults
        fc, configPath, err := loadFileConfig(".", cfg.ConfigPath)
        if err != nil {
            return err
        }
        f := cmd.Flags()
        // DSN
        if !f.Changed("dsn") && os.Getenv("PGY_DSN") == "" && fc.DSN != "" {
            cfg.DSN = fc.DSN
        }
        // schema-root
        if !f.Changed("schema-root") && os.Getenv("PGY_SCHEMA_ROOT") == "" && fc.SchemaRoot != "" {
            cfg.SchemaRoot = fc.SchemaRoot
        }
        // schemas
        if !f.Changed("schemas") && os.Getenv("PGY_SCHEMAS") == "" && len(cfg.Schemas) == 0 && len(fc.Schemas) > 0 {
            cfg.Schemas = fc.Schemas
        }
        // migrations-dir
        if !f.Changed("migrations-dir") && os.Getenv("PGY_MIGRATIONS_DIR") == "" && fc.MigrationsDir != "" {
            cfg.MigrationsDir = fc.MigrationsDir
        }
        // buffer
        if !f.Changed("buffer") && os.Getenv("PGY_BUFFER") == "" && fc.BufferPath != "" {
            cfg.BufferPath = fc.BufferPath
        }
        // quiet/verbose/json
        if !f.Changed("quiet") && os.Getenv("PGY_QUIET") == "" && fc.Quiet != nil {
            cfg.Quiet = *fc.Quiet
        }
        if !f.Changed("verbose") && os.Getenv("PGY_VERBOSE") == "" && fc.Verbose != nil {
            cfg.Verbose = *fc.Verbose
        }
        if !f.Changed("json") && os.Getenv("PGY_JSON") == "" && fc.JSON != nil {
            cfg.JSON = *fc.JSON
        }

        if len(cfg.Schemas) == 0 {
            // The config file is YAML too; keep it out of the schema list.
            absConfig := ""
            if configPath != "" {
                absConfig, _ = filepath.Abs(configPath)
            }
            // default: all .yml in schema-root
            _ = filepath.WalkDir(cfg.SchemaRoot, func(path string, d os.DirEntry, err error) error {
                if err != nil {
                    return nil
                }
                if d.IsDir() {
                    // Never descend into dot-dirs or dependency trees; walking an
                    // unpruned "." (e.g. $HOME) is pathologically slow.
                    n := d.Name()
                    if path != cfg.SchemaRoot && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor") {
                        return filepath.SkipDir
                    }
                    return nil
                }
                if strings.HasSuffix(d.Name(), ".yml") || strings.HasSuffix(d.Name(), ".yaml") {
                    if absConfig != "" {
                        if abs, absErr := filepath.Abs(path); absErr == nil && abs == absConfig {
                            return nil
                        }
                    }
                    rel, relErr := filepath.Rel(cfg.SchemaRoot, path)
                    if relErr == nil {
                        cfg.Schemas = append(cfg.Schemas, rel)
                    }
                }
                return nil
            })
        }
        ctx := context.WithValue(cmd.Context(), ctxKey{}, &cfg)
        cmd.SetContext(ctx)
        return nil
    }
}

type ctxKey struct{}

func FromContext(ctx context.Context) *GlobalConfig {
    v := ctx.Value(ctxKey{})
    if v == nil {
        return &GlobalConfig{}
    }
    return v.(*GlobalConfig)
}

func RegisterCommands(ctx context.Context, root *cobra.Command) {
    root.AddCommand(cmdInit())
    root.AddCommand(cmdDiff())
    root.AddCommand(cmdBuffer())
    root.AddCommand(cmdCommit())
    root.AddCommand(cmdMigrate())
    root.AddCommand(cmdMarkApplied())
    root.AddCommand(cmdStatus())
    root.AddCommand(&cobra.Command{Use: "version", Run: func(cmd *cobra.Command, args []string) { fmt.Println(VersionString()) }})
}

func splitCSV(v string) []string {
    if v == "" {
        return nil
    }
    parts := strings.Split(v, ",")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}

// version is set at build time via -ldflags "-X .../internal/cli.version=...".
// When it is empty (e.g. `go install`, `go run`, or a plain `go build`), it is
// derived from the module build info instead.
var version = ""

func VersionString() string {
    return fmt.Sprintf("pgy %s", resolveVersion())
}

func resolveVersion() string {
    if version != "" {
        return version
    }
    info, ok := debug.ReadBuildInfo()
    if !ok {
        return "dev"
    }
    // Module version is set for `go install module@version` builds.
    if v := info.Main.Version; v != "" && v != "(devel)" {
        return v
    }
    // Otherwise fall back to VCS stamps recorded by the toolchain.
    var revision, dirty string
    for _, s := range info.Settings {
        switch s.Key {
        case "vcs.revision":
            revision = s.Value
        case "vcs.modified":
            if s.Value == "true" {
                dirty = "-dirty"
            }
        }
    }
    if revision != "" {
        if len(revision) > 12 {
            revision = revision[:12]
        }
        return revision + dirty
    }
    return "dev"
}



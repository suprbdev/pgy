package cli

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/spf13/cobra"
)

type GlobalConfig struct {
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
    root.PersistentFlags().StringVar(&cfg.DSN, "dsn", getenv("PGY_DSN", ""), "PostgreSQL DSN")
    root.PersistentFlags().StringVar(&cfg.SchemaRoot, "schema-root", getenv("PGY_SCHEMA_ROOT", "."), "Schema root directory")
    root.PersistentFlags().StringSliceVar(&cfg.Schemas, "schemas", splitCSV(getenv("PGY_SCHEMAS", "")), "Comma-separated YAML schema files (relative to schema-root)")
    root.PersistentFlags().StringVar(&cfg.MigrationsDir, "migrations-dir", getenv("PGY_MIGRATIONS_DIR", "./migrations"), "Migrations directory")
    root.PersistentFlags().StringVar(&cfg.BufferPath, "buffer", getenv("PGY_BUFFER", "./.pgy.buffer.sql"), "Buffer SQL file path")
    root.PersistentFlags().BoolVar(&cfg.Quiet, "quiet", os.Getenv("PGY_QUIET") == "1", "Suppress non-essential output")
    root.PersistentFlags().BoolVar(&cfg.Verbose, "verbose", os.Getenv("PGY_VERBOSE") == "1", "Verbose output")
    root.PersistentFlags().BoolVar(&cfg.JSON, "json", os.Getenv("PGY_JSON") == "1", "JSON output where applicable")

    // store on context for subcommands
    root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
        // Load .pgy.yml and merge with precedence: flags > env > config > defaults
        fc, _ := loadFileConfig(".")
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
            // default: all .yml in schema-root
            _ = filepath.WalkDir(cfg.SchemaRoot, func(path string, d os.DirEntry, err error) error {
                if err != nil {
                    return nil
                }
                if d.IsDir() {
                    return nil
                }
                if strings.HasSuffix(d.Name(), ".yml") || strings.HasSuffix(d.Name(), ".yaml") {
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

var (
    version = "0.1.0"
    builtAt = ""
)

func VersionString() string {
    if builtAt == "" {
        return fmt.Sprintf("pgy %s", version)
    }
    return fmt.Sprintf("pgy %s (%s)", version, time.Unix(0, 0).UTC().Format(time.RFC3339))
}



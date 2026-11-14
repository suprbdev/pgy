package cli

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/validate"
)

func cmdValidate() *cobra.Command {
    var jsonOut bool
    c := &cobra.Command{
        Use:   "validate",
        Short: "Check schema files without a database",
        Long: `Check schema files without a database: every file against the YAML
JSON Schema, then the merged schema for missing columns, unresolvable
dependsOn entries, dependency cycles and likely mistakes.

Exit codes:
  0  No errors (warnings may be printed)
  1  Errors found`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg := FromContext(cmd.Context())
            paths, err := resolveSchemaPaths(cfg.SchemaRoot, cfg.Schemas)
            if err != nil { return err }
            if len(paths) == 0 { return fmt.Errorf("no schema files found under %s", cfg.SchemaRoot) }
            issues := validate.Files(paths)
            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                if err := enc.Encode(issues); err != nil { return err }
            } else {
                for _, i := range issues { fmt.Fprintln(cmd.OutOrStdout(), i) }
                if !cfg.Quiet && len(issues) == 0 { fmt.Fprintf(cmd.OutOrStdout(), "%d schema files OK\n", len(paths)) }
            }
            if validate.HasErrors(issues) {
                cmd.SilenceUsage = true
                return &exitCodeError{code: 1}
            }
            return nil
        },
    }
    c.Flags().BoolVar(&jsonOut, "json", false, "Print findings as JSON")
    return c
}

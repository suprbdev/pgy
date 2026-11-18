package cli

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/schema"
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
  1  Errors found, or warnings with --strict (unless ci.fail_on leaves out validate)`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg := FromContext(cmd.Context())
            paths, err := schemaFiles(cfg)
            if err != nil { return err }
            issues := validate.FilesWith(paths, cfg.External)
            var policyIssues []validate.Issue
            if !cfg.Policy.Empty() && !validate.HasErrors(issues) {
                if d, err := schema.LoadAndMerge(paths); err == nil {
                    d.External = cfg.External
                    policyIssues = validate.CheckPolicy(d, cfg.Policy)
                }
            }
            all := append(append([]validate.Issue{}, issues...), policyIssues...)
            if jsonOut || cfg.JSON {
                enc := json.NewEncoder(os.Stdout)
                enc.SetIndent("", "  ")
                if err := enc.Encode(all); err != nil { return err }
            } else {
                for _, i := range all { fmt.Println(i) }
                if !cfg.Quiet && len(all) == 0 { fmt.Printf("%d schema files OK\n", len(paths)) }
            }
            if validate.HasErrors(issues) {
                cmd.SilenceUsage = true
                return &exitCodeError{code: 1}
            }
            strict := &strictFindings{}
            if cfg.failsOn("validate") {
                for _, i := range issues { strict.add("validate", i.String()) }
            }
            if cfg.failsOn("policy") {
                for _, i := range policyIssues { strict.add("policy", i.String()) }
            }
            if err := strict.err(nil); err != nil {
                cmd.SilenceUsage = true
                return err
            }
            return nil
        },
    }
    c.Flags().BoolVar(&jsonOut, "json", false, "Print findings as JSON")
    return c
}

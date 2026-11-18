package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRoot runs pgy with args the way main does, config file and schema
// discovery included, from directory dir.
func runRoot(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd) //nolint:errcheck
	root := &cobra.Command{Use: "pgy", SilenceUsage: true, SilenceErrors: true}
	AttachGlobalFlags(root)
	RegisterCommands(context.Background(), root)
	root.SetArgs(args)
	return captureStdout(t, func() error { return root.ExecuteContext(context.Background()) })
}

// TestSchemaRootMustHoldSchemaFiles checks that diff and validate fail on
// a schema root that does not exist or holds no schema file, as in a
// project with only its config file, instead of comparing the database
// with an empty schema, while a file holding {} is an empty schema, and
// commands that read no schema files keep working without any.
func TestSchemaRootMustHoldSchemaFiles(t *testing.T) {
	for _, k := range []string{"PGY_SCHEMA_ROOT", "PGY_SCHEMAS", "PGY_CONFIG", "PGY_DSN"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	empty, onlyConfig, emptySchema := filepath.Join(dir, "empty"), filepath.Join(dir, "project"), filepath.Join(dir, "none")
	for _, d := range []string{empty, onlyConfig, emptySchema} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(onlyConfig, ".pgy.yml"), "migrations_dir: ./migrations\n")
	writeFile(t, filepath.Join(emptySchema, "schema.yml"), "{}\n")
	for _, c := range []struct {
		dir  string
		args []string
		want string
	}{
		{dir, []string{"diff", "--from-empty", "--buffer", "-", "--schema-root", filepath.Join(dir, "missing")}, "schema root " + filepath.Join(dir, "missing") + " does not exist"},
		{dir, []string{"diff", "--from-empty", "--buffer", "-", "--unsafe", "--prune", "--schema-root", filepath.Join(dir, "missing")}, "does not exist"},
		{dir, []string{"validate", "--schema-root", filepath.Join(dir, "missing")}, "does not exist"},
		{dir, []string{"diff", "--from-empty", "--buffer", "-", "--schema-root", empty}, "no schema files found under " + empty},
		{onlyConfig, []string{"diff", "--from-empty", "--buffer", "-"}, "no schema files found under ."},
		{onlyConfig, []string{"validate"}, "no schema files found under ."},
	} {
		if _, err := runRoot(t, c.dir, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: want an error with %q, got %v", c.args, c.want, err)
		}
	}
	if out, err := runRoot(t, emptySchema, "diff", "--from-empty", "--buffer", "-"); err != nil || strings.TrimSpace(out) != "" {
		t.Errorf("a file holding {} is an empty schema: %q, %v", out, err)
	}
	writeFile(t, filepath.Join(onlyConfig, ".pgy.buffer.sql"), "create table t (id int);\n")
	if _, err := runRoot(t, onlyConfig, "commit", "first", "--quiet"); err != nil {
		t.Errorf("commit reads no schema files: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(onlyConfig, "migrations", "0001_first.sql")); len(files) != 1 {
		t.Errorf("commit wrote no migration, got %v", files)
	}
}

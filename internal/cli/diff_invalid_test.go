package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffFailsOnSchemaErrors(t *testing.T) {
	for _, args := range [][]string{{"--from-empty"}, {"--from-empty", "--no-warnings"}} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "s.yml"), "schema app:\n  table t:\n    columns:\n      a: {type: int}\n    constraints:\n      c1: {type: check, check: \"a > 0\"}\n")
		buffer := filepath.Join(dir, "buffer.sql")
		writeFile(t, buffer, "-- earlier buffer\n")
		cfg := &GlobalConfig{SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: buffer, Quiet: true}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		_, runErr := captureStdout(t, func() error {
			c := cmdDiff()
			c.SetArgs(args)
			c.SilenceUsage, c.SilenceErrors = true, true
			return c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
		})
		os.Stderr = old
		_ = w.Close()
		stderr, _ := io.ReadAll(r)
		if code, ok := AsExitCode(runErr); !ok || code != 1 {
			t.Errorf("%v: want exit 1, got %v", args, runErr)
		}
		for _, want := range []string{"additionalProperties 'check' not allowed", "constraint c1 has no expression"} {
			if !strings.Contains(string(stderr), want) {
				t.Errorf("%v: missing %q in stderr:\n%s", args, want, stderr)
			}
		}
		if strings.Contains(string(stderr), "warning:") {
			t.Errorf("%v: schema errors must not be reported as warnings:\n%s", args, stderr)
		}
		if got, _ := os.ReadFile(buffer); string(got) != "-- earlier buffer\n" {
			t.Errorf("%v: the buffer must be left as it was, got:\n%s", args, got)
		}
	}
}

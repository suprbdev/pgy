package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPullRejectsSchemasFlag(t *testing.T) {
	root := &cobra.Command{Use: "pgy", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringSlice("schemas", nil, "")
	pull := cmdPull()
	root.AddCommand(pull)
	root.SetArgs([]string{"pull", "--schemas", "app"})
	err := root.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, &GlobalConfig{DSN: "postgres://unused"}))
	if err == nil || !strings.Contains(err.Error(), "use --schema") {
		t.Fatalf("expected a --schema hint, got %v", err)
	}
}

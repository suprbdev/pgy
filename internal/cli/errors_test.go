package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorDetail(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "2BP01", Message: "cannot drop table t because other objects depend on it",
		Detail: "view v depends on table t", Hint: "Use DROP ... CASCADE to drop the dependent objects too."}
	got := ErrorDetail(fmt.Errorf("0001_x.sql: %w", pgErr))
	if want := "\nDETAIL: view v depends on table t\nHINT: Use DROP ... CASCADE to drop the dependent objects too."; got != want {
		t.Errorf("ErrorDetail = %q, want %q", got, want)
	}
	if got := ErrorDetail(&pgconn.PgError{Message: "boom"}); got != "" {
		t.Errorf("an error without detail or hint adds nothing, got %q", got)
	}
	if got := ErrorDetail(errors.New("checksum mismatch")); got != "" {
		t.Errorf("a non-PostgreSQL error adds nothing, got %q", got)
	}
}

func TestMigrateErrorCarriesDetailAndHint(t *testing.T) {
	const sch = "pgytest_cli_errdetail"
	pool, commitAndMigrate := migrationSandbox(t, sch)
	t.Cleanup(func() { pool.Exec(context.Background(), `drop schema if exists `+sch+`_obj cascade`) }) //nolint
	err := commitAndMigrate("create schema " + sch + "_obj;\ncreate table " + sch + "_obj.t (id int);\n" +
		"create view " + sch + "_obj.v as select id from " + sch + "_obj.t;\ndrop table " + sch + "_obj.t;\n")
	if err == nil {
		t.Fatal("dropping a table a view depends on must fail")
	}
	detail := ErrorDetail(err)
	if !strings.Contains(detail, "\nDETAIL: view "+sch+"_obj.v depends on table "+sch+"_obj.t") || !strings.Contains(detail, "\nHINT: ") {
		t.Errorf("migrate error %q carries detail %q, want PostgreSQL's DETAIL and HINT", err, detail)
	}
}

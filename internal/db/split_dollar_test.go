package db

import "testing"

func TestSplitSQLNestedDollarQuotes(t *testing.T) {
	sql := "create function f() returns int language plpgsql as $body$\nbegin execute $$select 1; select 2$$; return 1; end\n$body$;\nselect 3;"
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("got %d statements: %q", len(got), got)
	}
}

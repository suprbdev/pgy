package db

import "testing"

func TestSplitSQLNestedDollarQuotes(t *testing.T) {
	sql := "create function f() returns int language plpgsql as $body$\nbegin execute $$select 1; select 2$$; return 1; end\n$body$;\nselect 3;"
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("got %d statements: %q", len(got), got)
	}
}

func TestSplitSQLEscapeStrings(t *testing.T) {
	for sql, want := range map[string]int{
		`insert into t values (E'it\'s; fine'); select 1;`:   2,
		`insert into t values (e'a\\'); select 2;`:           2,
		`insert into t values (E'x''y; z'); select 3;`:       2,
		`select date'2020-01-01'; select 'a''b;c';`:          2,
		`comment on table t is E'line\nnext; done'; select 4`: 2,
	} {
		if got := SplitSQLStatements(sql); len(got) != want {
			t.Errorf("%s: got %d statements %q", sql, len(got), got)
		}
	}
}

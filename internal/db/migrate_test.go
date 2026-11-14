package db

import (
	"strings"
	"testing"
)

func TestSplitSemicolonInSingleQuote(t *testing.T) {
	sql := `comment on table "t" is 'first; second';
create table x (id int);`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
	if got[0] != `comment on table "t" is 'first; second'` {
		t.Errorf("statement 0 split mid-string: %q", got[0])
	}
}

func TestSplitEscapedSingleQuote(t *testing.T) {
	sql := `comment on table t is 'it''s here; still one';
select 1;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "it''s here; still one") {
		t.Errorf("escaped quote broke split: %q", got[0])
	}
}

func TestSplitSemicolonInDoubleQuotedIdent(t *testing.T) {
	sql := `create table "weird;name" (id int);
select 1;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
}

func TestSplitDollarQuoteWithSemicolonAndQuote(t *testing.T) {
	sql := `create function f() returns int language plpgsql as $$
begin
	raise notice 'x; y';
	return 1;
end
$$;
select 2;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "return 1;") {
		t.Errorf("dollar body split: %q", got[0])
	}
}

func TestSplitTaggedDollarQuote(t *testing.T) {
	sql := `create function f() returns int as $fn$select 1; -- ;$fn$ language sql;`
	got := SplitSQLStatements(sql)
	if len(got) != 1 {
		t.Fatalf("want 1 statement, got %d: %v", len(got), got)
	}
}

func TestSplitLineCommentWithSemicolon(t *testing.T) {
	sql := `select 1 -- trailing; not a terminator
+ 1;
select 2;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
}

func TestSplitBlockCommentWithSemicolonNested(t *testing.T) {
	sql := `select 1 /* outer; /* inner; */ still outer; */;
select 2;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %v", len(got), got)
	}
}

func TestSplitPositionalParamNotDollarQuote(t *testing.T) {
	sql := `select $1; select $2;`
	got := SplitSQLStatements(sql)
	if len(got) != 2 {
		t.Fatalf("$1 must not start a dollar quote; got %d: %v", len(got), got)
	}
}

func TestSplitNoTrailingSemicolon(t *testing.T) {
	got := SplitSQLStatements("select 1; select 2")
	if len(got) != 2 || got[1] != "select 2" {
		t.Fatalf("want trailing statement kept, got %v", got)
	}
}

func TestSplitEmptyAndWhitespace(t *testing.T) {
	got := SplitSQLStatements(" ;;  ;\n")
	if len(got) != 0 {
		t.Fatalf("want no statements, got %v", got)
	}
}

func TestSplitSemicolonInsideParentheses(t *testing.T) {
	sql := `create rule r as on insert to t do also (insert into log values (new.id); notify t_changed);
select ')' as paren; select 1;`
	got := SplitSQLStatements(sql)
	if len(got) != 3 {
		t.Fatalf("want 3 statements, got %d: %v", len(got), got)
	}
	if !strings.HasSuffix(got[0], "notify t_changed)") {
		t.Errorf("multi-action rule split mid-statement: %q", got[0])
	}
}

func TestHasNoTransactionDirective(t *testing.T) {
	cases := map[string]bool{
		"-- pgy 1.0\n-- checksum x\n\n-- pgy:no-transaction\ncreate index concurrently i on t (a);\n": true,
		"-- pgy:no-transaction\nselect 1;\n":                 true,
		"select 1;\n-- pgy:no-transaction\n":                 false,
		"-- pgy 1.0\n\ncreate table t (a int);\n":            false,
		"  -- pgy:no-transaction  \n\nvacuum t;":             true,
	}
	for sql, want := range cases {
		if got := HasNoTransactionDirective(sql); got != want {
			t.Errorf("HasNoTransactionDirective(%q) = %v, want %v", sql, got, want)
		}
	}
}

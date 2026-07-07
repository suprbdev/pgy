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

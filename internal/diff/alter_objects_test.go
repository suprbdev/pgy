package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func seqDB(sq *schema.Sequence) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Sequences: map[string]*schema.Sequence{"public.s": sq}}
}

func liveSeq(ls *LiveSequence) *Live {
	l := emptyLive()
	l.Sequences["public.s"] = true
	l.SequenceDefs = map[string]*LiveSequence{"public.s": ls}
	return l
}

func TestSequenceAltered(t *testing.T) {
	live := liveSeq(&LiveSequence{Type: "integer", Increment: "1", MinValue: "1", MaxValue: "2147483647", Start: "1", Cache: "1", Owner: "public.t.id"})
	sq := &schema.Sequence{Name: "s", As: "bigint", Increment: "5", MaxValue: "1000000", Cache: "20", Cycle: schema.BoolPtr(true), OwnedBy: "orders.id"}
	p := Plan(live, seqDB(sq), false)
	want := []string{
		`alter sequence "public"."s" as bigint increment by 5 maxvalue 1000000 cache 20 cycle;`,
		`alter sequence "public"."s" owned by "orders"."id";`,
	}
	if len(p.Alters) != 2 || p.Alters[0] != want[0] || p.Alters[1] != want[1] {
		t.Errorf("want %v, got %v", want, p.Alters)
	}
}

func TestSequenceSameOrUnmanagedSkipped(t *testing.T) {
	live := liveSeq(&LiveSequence{Type: "bigint", Increment: "5", MinValue: "1", MaxValue: "1000", Start: "1", Cache: "20", Cycle: true, Owner: "public.orders.id"})
	sq := &schema.Sequence{Name: "s", As: "int8", Increment: "5", MaxValue: "1000", Cycle: schema.BoolPtr(true), OwnedBy: "public.orders.id"}
	if sql := Render(Plan(live, seqDB(sq), false)); sql != "" {
		t.Errorf("same options must not diff, got:\n%s", sql)
	}
	if sql := Render(Plan(live, seqDB(&schema.Sequence{Name: "s"}), false)); sql != "" {
		t.Errorf("unset options are unmanaged, got:\n%s", sql)
	}
	if p := Plan(live, seqDB(&schema.Sequence{Name: "s", Cycle: schema.BoolPtr(false), OwnedBy: "none"}), false); len(p.Alters) != 2 {
		t.Errorf("expected no cycle + owned by none, got %v", p.Alters)
	}
}

func domDB(dm *schema.Domain) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Domains: map[string]*schema.Domain{"public.email": dm}}
}

func liveDom(ld *LiveDomain) *Live {
	l := emptyLive()
	l.Domains["public.email"] = true
	l.DomainDefs = map[string]*LiveDomain{"public.email": ld}
	return l
}

func TestDomainAltered(t *testing.T) {
	live := liveDom(&LiveDomain{Default: "'a@b'::text", NotNull: false, Checks: map[string]string{"email_check": "CHECK ((VALUE ~~ '%@%'::text))"}})
	dm := &schema.Domain{Name: "email", Type: "text", Default: "'x@y'", NotNull: true, Check: "value ~ '@'"}
	if p := Plan(live, domDB(dm), false); len(p.Alters) != 2 {
		t.Errorf("without --unsafe only default and not null change, got %v", p.Alters)
	}
	p := Plan(live, domDB(dm), true)
	want := []string{
		`alter domain "public"."email" set default 'x@y';`,
		`alter domain "public"."email" set not null;`,
		`alter domain "public"."email" drop constraint "email_check";`,
		`alter domain "public"."email" add constraint "email_check" check (value ~ '@');`,
	}
	if len(p.Alters) != 4 {
		t.Fatalf("want %v, got %v", want, p.Alters)
	}
	for i := range want {
		if p.Alters[i] != want[i] {
			t.Errorf("alter %d: got %q, want %q", i, p.Alters[i], want[i])
		}
	}
}

func TestDomainSameSkippedAndRemovals(t *testing.T) {
	live := liveDom(&LiveDomain{Default: "'a@b'::text", NotNull: true, Checks: map[string]string{"email_ok": "CHECK ((VALUE ~~ '%@%'::text))"}})
	dm := &schema.Domain{Name: "email", Type: "text", Default: "'a@b'", NotNull: true, Check: "VALUE like '%@%'", ConstraintName: "email_ok"}
	if sql := Render(Plan(live, domDB(dm), true)); sql != "" {
		t.Errorf("equivalent domain must not diff, got:\n%s", sql)
	}
	bare := &schema.Domain{Name: "email", Type: "text", ConstraintName: "email_ok"}
	p := Plan(live, domDB(bare), true)
	want := []string{
		`alter domain "public"."email" drop default;`,
		`alter domain "public"."email" drop not null;`,
		`alter domain "public"."email" drop constraint "email_ok";`,
	}
	if len(p.Alters) != 3 || p.Alters[0] != want[0] || p.Alters[1] != want[1] || p.Alters[2] != want[2] {
		t.Errorf("want %v, got %v", want, p.Alters)
	}
}

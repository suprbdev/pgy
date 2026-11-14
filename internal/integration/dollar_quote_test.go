package integration

import "testing"

func TestIntegrationBodiesContainingDollarQuotes(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"dollars": `
schema SCH:
  function dyn(n int):
    returns: int
    language: plpgsql
    body: |
      declare r int;
      begin
        execute $$select $1 * 2$$ into r using n;
        return r;
      end
  function tagged():
    returns: text
    language: sql
    body: select $body$it costs $5$body$
  procedure noop():
    language: plpgsql
    body: |
      begin
        perform $$x$$;
      end
`,
	})
}

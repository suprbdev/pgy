package integration

import "testing"

func TestIntegrationSQLFunctionBodiesReadingTables(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"reads_table": `
schema SCH:
  function current_tenant():
    returns: uuid
    language: sql
    stable: true
    body: select tenant_id from SCH.sessions limit 1
  procedure purge():
    language: sql
    body: delete from SCH.sessions
  table sessions:
    columns:
      id: {type: int, primaryKey: true}
      tenant_id: {type: uuid, nullable: true}
`,
		"default_reads_own_table": `
schema SCH:
  function next_id():
    returns: int
    language: sql
    body: select coalesce(max(id), 0) + 1 from SCH.items
  table items:
    columns:
      id: {type: int, primaryKey: true, default: SCH.next_id()}
`,
	})
}

func TestIntegrationRoutineBodiesUncheckedAtCreate(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"plpgsql_rowtype_of_later_table": `
schema SCH:
  function first_id():
    returns: int
    language: plpgsql
    body: |
      declare
        r SCH.items%rowtype;
      begin
        select * into r from SCH.items order by id limit 1;
        return r.id;
      end;
  table items:
    columns:
      id: {type: int, primaryKey: true}
`,
		"sql_calls_later_routine": `
schema SCH:
  function a_total():
    returns: int
    language: sql
    body: select SCH.b_part() + 1
  function b_part():
    returns: int
    language: sql
    body: select 1
`,
	})
}

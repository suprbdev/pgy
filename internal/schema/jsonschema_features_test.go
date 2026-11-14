package schema

import "testing"

func TestJSONSchemaAcceptsExtendedFeatures(t *testing.T) {
    s := compileEditorSchema(t)
    doc := `
extensions:
  - {name: pg_trgm, schema: extensions, version: "1.6", cascade: true}
publications:
  cdc:
    tables:
      - public.users
      - {table: public.orders, columns: [id, status], where: "status <> 'draft'"}
    schemas: [billing]
    publish: [insert, update]
    viaPartitionRoot: true
    comment: cdc
  all: {allTables: true}
eventTriggers:
  log_ddl: {event: ddl_command_end, tags: [CREATE TABLE], function: public.log_ddl(), enabled: always, comment: audit}
schema public:
  collation ci: {provider: icu, locale: und-u-ks-level2, deterministic: false, comment: ci}
  collation c_copy: {from: pg_catalog.C}
  type floatrange: {type: range, subtype: float8, subtypeDiff: float8mi, multirangeTypeName: floatmultirange}
  sequence order_no: {as: bigint, cycle: false, ownedBy: none}
  table orders:
    unlogged: false
    with: {fillfactor: 80, autovacuum_enabled: false}
    columns:
      id: {type: int}
      price: {type: numeric}
      qty: {type: int}
      total: {type: numeric, generated: price * qty}
      doubled: {type: int, generated: qty * 2, virtual: true}
      code: {type: text, collate: C, compression: lz4, storage: external}
      pid: {type: int, nullable: true}
      valid: {type: tstzrange}
    primaryKey: [id, valid without overlaps]
    indexes:
      orders_cover: {columns: [id desc nulls last], unique: true, include: [price], nullsNotDistinct: true, with: {fillfactor: 70}}
    foreignKeys:
      orders_pid_fkey:
        columns: [pid, period valid]
        references: {table: public.orders, columns: [id, period valid]}
        onUpdate: cascade
        match: full
        deferrable: true
        initiallyDeferred: true
        notValid: true
        enforced: false
    constraints:
      orders_qty_check: {type: check, expression: qty > 0, noInherit: true, notValid: true, enforced: false, comment: "@name qty"}
      orders_code_key: {type: unique, columns: [code], nullsNotDistinct: true, deferrable: true, initiallyDeferred: true}
    triggers:
      orders_audit:
        timing: after
        events: [insert, "update of price, qty"]
        referencing: {old: old_rows, new: new_rows}
        procedure: public.audit()
        comment: audit trail
    policies:
      tenant: {as: restrictive, using: "true", comment: tenant isolation}
  view active_orders:
    query: select 1
    triggers:
      active_orders_ins: {timing: instead of, events: [insert], procedure: public.ins(), comment: writable}
    securityInvoker: true
    securityBarrier: false
    checkOption: cascaded
  materialized view order_stats:
    query: select 1 as id
    withData: false
    indexes:
      order_stats_id: {columns: [id], unique: true}
`
    if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err != nil {
        t.Errorf("document using the extended features rejected:\n%v", err)
    }
}

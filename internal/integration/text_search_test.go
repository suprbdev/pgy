package integration

import "testing"

func TestIntegrationTextSearchIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"text_search": `
extensions: [unaccent]
schema SCH:
  text search dictionary english_nostop:
    template: snowball
    options: {language: english}
    comment: english stemming without stopwords
  text search configuration english_unaccent:
    copy: pg_catalog.english
    mappings:
      asciiword: [unaccent, SCH.english_nostop]
      word: [unaccent, SCH.english_nostop]
      email: []
    comment: accent-insensitive english
  table docs:
    columns:
      body: {type: text}
      tsv:
        type: tsvector
        nullable: true
        generated: "to_tsvector('SCH.english_unaccent'::regconfig, body)"
    indexes:
      docs_tsv:
        columns: [tsv]
        using: gin
`,
	})
}

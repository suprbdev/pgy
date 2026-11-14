// Package schemas embeds the JSON Schema for pgy YAML files, published for
// yaml-language-server and used by pgy validate.
package schemas

import _ "embed"

// JSONSchema is the contents of pgy.schema.json.
//
//go:embed pgy.schema.json
var JSONSchema []byte

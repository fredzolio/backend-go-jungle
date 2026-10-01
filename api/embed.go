// Package api embeds the OpenAPI contract served at /openapi.yaml.
package api

import _ "embed"

// OpenAPI is the HTTP contract.
//
//go:embed openapi.yaml
var OpenAPI []byte

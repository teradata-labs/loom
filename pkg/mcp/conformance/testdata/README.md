# Vendored MCP schema

`schema.2026-07-28.json` is the official MCP schema for protocol revision
2026-07-28, vendored so conformance tests pin the schema version instead of
tracking a moving URL.

- Source: https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/schema/2026-07-28/schema.json
- Retrieved: 2026-08-18

`schema_tripwire_test.go` asserts that every wire identifier `pkg/mcp` emits
exists in this schema; a failure there means either local drift or an
upstream schema change worth reading.

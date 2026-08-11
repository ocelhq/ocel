// Lean shared module: the edge contract — the interfaces and value types a
// cloud provider talks to its edge through, and that an edge implementation
// satisfies. It deliberately carries no cloud SDK (and no dependencies at all)
// so a provider can implement against it without inheriting any particular
// edge's or cloud's dependency graph.
module github.com/ocelhq/ocel/platform/edge/contract

go 1.25.11

require github.com/ocelhq/ocel/pkg/naming v0.0.0-00010101000000-000000000000

replace github.com/ocelhq/ocel/pkg/naming => ../../../pkg/naming

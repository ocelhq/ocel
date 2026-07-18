// Lean shared module: the edge contract — the interfaces and value types a
// cloud provider talks to its edge through, and that an edge implementation
// satisfies. It deliberately carries no cloud SDK (and no dependencies at all)
// so a provider can implement against it without inheriting any particular
// edge's or cloud's dependency graph.
module github.com/ocelhq/ocel/cloud/edge

go 1.25.11

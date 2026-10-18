package function

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

type factsEdge struct {
	edge.Edge
	facts edge.Facts
}

func (f factsEdge) Facts() edge.Facts { return f.facts }

func fakeEdgeOf(kind edge.Kind) edge.Edge {
	declared, err := edges.EdgeFor(kind, edges.Deps{})
	if err != nil {
		panic("fakeEdgeOf: " + err.Error())
	}
	real := declared.Facts()
	facts := edge.Facts{
		Supported:             real.Supported,
		RunsCode:              true,
		ServesUnbound:         real.ServesUnbound,
		ProxiesRecords:        real.ProxiesRecords,
		ProxiedRecordNote:     real.ProxiedRecordNote,
		InvalidatesByCacheTag: real.InvalidatesByCacheTag,
		CachesResponses:       real.CachesResponses,
		Gates:                 true,
	}
	if !slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool { return slices.Contains(facts.Supported, need) }) {
		facts.RunsCode = false
	}
	return factsEdge{Edge: declared, facts: facts}
}

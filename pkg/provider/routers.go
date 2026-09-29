package provider

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
)

type Routers interface {
	Open(kind router.Kind) (router.Router, error)
}

type Pairing struct {
	Edge     edge.Kind
	Router   router.Kind
	Computes []Compute
}

func (f Facts) PairedRouter(front edge.Kind, compute Compute) (router.Kind, bool) {
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && slices.Contains(pairing.Computes, compute) {
			return pairing.Router, true
		}
	}
	return "", false
}

func (f Facts) PairedRouters(front edge.Kind) []router.Kind {
	var paired []router.Kind
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && !slices.Contains(paired, pairing.Router) {
			paired = append(paired, pairing.Router)
		}
	}
	return paired
}

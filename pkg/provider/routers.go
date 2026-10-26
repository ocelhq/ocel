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
	Edge      edge.Kind
	Router    router.Kind
	Computes  []Compute
	Forwarded bool
	FrontedBy router.Kind
}

func (f Facts) PairedRouter(front edge.Kind, compute Compute) (router.Kind, bool) {
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && slices.Contains(pairing.Computes, compute) {
			return pairing.Router, true
		}
	}
	return "", false
}

func (f Facts) ListPairedRouters(front edge.Kind) []router.Kind {
	var paired []router.Kind
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && !slices.Contains(paired, pairing.Router) {
			paired = append(paired, pairing.Router)
		}
	}
	return paired
}

func (f Facts) FindEdgeRouter(front edge.Kind) (router.Kind, bool) {
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && !pairing.Forwarded {
			return pairing.Router, true
		}
	}
	return "", false
}

func (f Facts) FindFrontingRouter(front edge.Kind, kind router.Kind) (router.Kind, bool) {
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && pairing.Router == kind && pairing.FrontedBy != "" {
			return pairing.FrontedBy, true
		}
	}
	return "", false
}

func (f Facts) IsForwarded(front edge.Kind, kind router.Kind) bool {
	return slices.ContainsFunc(f.Pairings, func(pairing Pairing) bool {
		return pairing.Edge == front && pairing.Router == kind && pairing.Forwarded
	})
}

func (f Facts) ListFrontingPairings(front edge.Kind) []Pairing {
	pairings := make([]Pairing, 0, len(f.Pairings)+1)
	var fronting []Pairing
	for _, pairing := range f.Pairings {
		if pairing.Edge == front && pairing.FrontedBy != "" {
			if !slices.ContainsFunc(fronting, func(own Pairing) bool { return own.Router == pairing.FrontedBy }) &&
				!slices.ContainsFunc(f.Pairings, func(own Pairing) bool { return own.Edge == front && own.Router == pairing.FrontedBy }) {
				fronting = append(fronting, Pairing{Edge: front, Router: pairing.FrontedBy})
			}
			pairing.Forwarded = true
		}
		pairings = append(pairings, pairing)
	}
	return slices.Concat(fronting, pairings)
}

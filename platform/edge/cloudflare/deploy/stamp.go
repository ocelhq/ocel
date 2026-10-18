package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
)

type stampedSpec struct {
	Generic         workerScript
	GenericName     string
	Slug            string
	StoreScriptName string
	StoreEndpoint   string
	Domains         []string
	PruneOnly       bool
	PruneRoutes     bool
	PruneWorkerStem string
	CompatDate      string
	CompatFlags     []string
	Observability   map[string]any
}

func specStamp(spec edge.StackSpec, program entryProgram, generic workerScript) (string, error) {
	sum := sha256.New()
	if err := json.NewEncoder(sum).Encode(stampedSpec{
		Generic:         generic,
		GenericName:     program.name,
		Slug:            spec.Slug,
		StoreScriptName: program.store.Name,
		StoreEndpoint:   program.store.Endpoint,
		Domains:         spec.Domains,
		PruneOnly:       spec.PruneOnly,
		PruneRoutes:     spec.PruneRoutes,
		PruneWorkerStem: program.pruneWorkerStem,
		CompatDate:      compatDate,
		CompatFlags:     compatFlags,
		Observability:   observability(),
	}); err != nil {
		return "", fmt.Errorf("hash stack spec: %w", err)
	}
	return spec.Version + "." + hex.EncodeToString(sum.Sum(nil)), nil
}

type stampSet map[string]string

func decodeStampSet(raw string) stampSet {
	set := stampSet{}
	if raw == "" {
		return set
	}
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		return stampSet{}
	}
	return set
}

func (s stampSet) encode() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode stack version stamps: %w", err)
	}
	return string(encoded), nil
}

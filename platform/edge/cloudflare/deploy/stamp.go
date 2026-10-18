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
		CompatDate:      compatDate,
		CompatFlags:     compatFlags,
		Observability:   observability(),
	}); err != nil {
		return "", fmt.Errorf("hash stack spec: %w", err)
	}
	return spec.Version + "." + hex.EncodeToString(sum.Sum(nil)), nil
}

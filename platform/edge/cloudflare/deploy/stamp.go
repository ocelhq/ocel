package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

type stampedSpec struct {
	Generic         edge.Worker
	GenericName     string
	Slug            string
	StoreScriptName string
	StoreEndpoint   string
	Domains         []string
	PruneOnly       bool
	PruneRoutes     bool
	PruneWorkerStem string
	RequiredRecord  string
	CompatDate      string
	CompatFlags     []string
	Observability   map[string]any
	PreviewGate     *previewgate.Gate
}

func specStamp(spec edge.StackSpec, generic edge.Worker) (string, error) {
	sum := sha256.New()
	if err := json.NewEncoder(sum).Encode(stampedSpec{
		Generic:         generic,
		GenericName:     spec.Program.Name,
		Slug:            spec.Slug,
		StoreScriptName: spec.Program.StoreScriptName,
		StoreEndpoint:   spec.Program.StoreEndpoint,
		Domains:         spec.Domains,
		PruneOnly:       spec.PruneOnly,
		PruneRoutes:     spec.PruneRoutes,
		PruneWorkerStem: spec.Program.PruneWorkerStem,
		RequiredRecord:  spec.Program.RequiredRecord,
		CompatDate:      compatDate,
		CompatFlags:     compatFlags,
		Observability:   observability(),
		PreviewGate:     spec.PreviewGate,
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

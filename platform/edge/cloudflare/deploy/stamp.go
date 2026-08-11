package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const envSkipEdgeReconcile = "OCEL_SKIP_EDGE_RECONCILE"

func skipEdgeReconcile() bool {
	switch strings.ToLower(os.Getenv(envSkipEdgeReconcile)) {
	case "1", "true":
		return true
	default:
		return false
	}
}

type stampedSpec struct {
	Generic             edge.Worker
	GenericName         string
	Slug                string
	StoreScriptName     string
	ISRWriterScriptName string
	StoreEndpoint       string
	Domains             []string
	PruneRoutes         bool
	PruneWorkerStem     string
	RequiredRecord      string
	Values              map[string]string
}

func specStamp(spec edge.RootStackSpec, generic edge.Worker) (string, error) {
	sum := sha256.New()
	if err := json.NewEncoder(sum).Encode(stampedSpec{
		Generic:             generic,
		GenericName:         spec.GenericName,
		Slug:                spec.Slug,
		StoreScriptName:     spec.StoreScriptName,
		ISRWriterScriptName: spec.ISRWriterScriptName,
		StoreEndpoint:       spec.StoreEndpoint,
		Domains:             spec.Domains,
		PruneRoutes:         spec.PruneRoutes,
		PruneWorkerStem:     spec.PruneWorkerStem,
		RequiredRecord:      spec.RequiredRecord,
		Values:              spec.Values,
	}); err != nil {
		return "", fmt.Errorf("hash root-stack spec: %w", err)
	}
	return spec.Version + "." + hex.EncodeToString(sum.Sum(nil)), nil
}

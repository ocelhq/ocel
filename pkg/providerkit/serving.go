package providerkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const FrameworkNext = "next"

const MembranePrefix = "ocel-membrane-layer"

type MembraneSource interface {
	Membrane(ctx context.Context) ([]byte, error)
}

type ServingQuery struct {
	Root         string
	Project      string
	App          string
	Framework    string
	Stack        naming.StackName
	Coordinate   naming.Coordinate
	EdgeRunsCode bool
}

type ServingFacts struct {
	Routing     *RoutingPlan
	ISR         *ISRPlan
	Bytecode    *BytecodePlan
	AssetPrefix string
}

func ServingFactsFor(q ServingQuery) (ServingFacts, error) {
	facts := ServingFacts{
		AssetPrefix: q.Coordinate.AssetKey(""),
		Bytecode:    &BytecodePlan{Prefix: withoutSlash(q.Coordinate.BytecodePrefix())},
	}
	if q.Framework == FrameworkNext {
		facts.ISR = &ISRPlan{
			Prefix:       withoutSlash(q.Coordinate.ISRPrefix()),
			TagNamespace: naming.ISRTagPrefix(q.Project, q.Stack),
		}
	}
	routing, err := routingFor(q)
	if err != nil {
		return ServingFacts{}, err
	}
	facts.Routing = routing
	return facts, nil
}

func routingFor(q ServingQuery) (*RoutingPlan, error) {
	if q.EdgeRunsCode {
		return nil, nil
	}
	desc, present, err := ReadServeDescriptor(q.Root, q.App)
	if err != nil {
		return nil, err
	}
	if !present || !desc.EdgeRouting {
		return nil, nil
	}
	if desc.Entry == "" {
		return nil, Refuse(CodeInvalid,
			"app %s routes at its origin but its build names no entry route; rebuild the app", q.App)
	}
	raw, err := os.ReadFile(filepath.Join(AppArtifactRoot(q.Root, q.App), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Refuse(CodeInvalid,
			"app %s routes at its origin but its build wrote no %s; rebuild the app", q.App, edge.RoutingManifestFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the routing manifest %s hosts at its origin: %w", q.App, err)
	}
	return &RoutingPlan{Entry: desc.Entry, Manifest: raw}, nil
}

func PlaceMembrane(ctx context.Context, source MembraneSource, store ArtifactStore, report Reporter) (ArtifactRef, error) {
	if source == nil {
		return ArtifactRef{}, nil
	}
	body, err := source.Membrane(ctx)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("read the membrane the app's functions boot through: %w", err)
	}
	if len(body) == 0 {
		return ArtifactRef{}, Refuse(CodeNotReady,
			"this provider carries no membrane for an app's functions to boot through")
	}
	sum := sha256.Sum256(body)
	ref := ArtifactRef{Bucket: StoreFunctions, Key: MembraneKey(hex.EncodeToString(sum[:]))}
	if held, err := store.Open(ctx, ref); err == nil {
		return ref, held.Close()
	}
	if err := store.Put(ctx, ref, bytes.NewReader(body)); err != nil {
		return ArtifactRef{}, fmt.Errorf("place the membrane: %w", err)
	}
	if report != nil {
		report.Detail("placed the membrane at " + ref.Key)
	}
	return ref, nil
}

func MembraneKey(digest string) string {
	return MembranePrefix + "/" + digest + ".zip"
}

func withoutSlash(prefix string) string {
	return strings.TrimSuffix(prefix, naming.PathSeparator)
}

package providerserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type AppServingInput struct {
	Root              string
	Project           string
	App               string
	Framework         string
	Stack             naming.StackName
	Coordinate        naming.Coordinate
	EdgeRunsCode      bool
	EdgeSignsForwards bool
}

type AppServing struct {
	Entry       string
	Routing     *provider.RoutingSpec
	EdgeRouting *provider.RoutingSpec
	Guard       *provider.OriginGuard
	ISR         *provider.ISRSpec
	Bytecode    *provider.BytecodeSpec
	AssetPrefix string
}

func AppServingFor(q AppServingInput) (AppServing, error) {
	desc, present, err := appbuild.ReadServeDescriptor(q.Root, q.App)
	if err != nil {
		return AppServing{}, err
	}
	facts := AppServing{
		AssetPrefix: q.Coordinate.AssetKey(""),
		Bytecode:    &provider.BytecodeSpec{Prefix: withoutSlash(q.Coordinate.BytecodePrefix())},
	}
	if present {
		facts.Entry = desc.Entry
	}
	if q.Framework == appbuild.FrameworkNext {
		facts.ISR = &provider.ISRSpec{
			Prefix:       withoutSlash(q.Coordinate.ISRPrefix()),
			TagNamespace: naming.ISRTagPrefix(q.Project, q.Stack),
		}
	}
	routing, err := routingFor(q, desc, present)
	if err != nil {
		return AppServing{}, err
	}
	if q.EdgeRunsCode {
		facts.EdgeRouting = routing
	} else {
		facts.Routing = routing
	}
	facts.Guard = guardFor(q, desc, present)
	return facts, nil
}

func guardFor(q AppServingInput, desc edge.ServeDescriptor, present bool) *provider.OriginGuard {
	if q.EdgeRunsCode || q.EdgeSignsForwards || !present || desc.Entry == "" {
		return nil
	}
	return &provider.OriginGuard{Entry: desc.Entry}
}

func anyProxied(proxied func(provider.BindingType) bool, grants []provider.Binding) bool {
	return slices.ContainsFunc(grants, func(binding provider.Binding) bool { return proxied(binding.Type) })
}

func routingFor(q AppServingInput, desc edge.ServeDescriptor, present bool) (*provider.RoutingSpec, error) {
	if !present || !desc.EdgeRouting {
		return nil, nil
	}
	if desc.Entry == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build names no entry route; rebuild the app", q.App)
	}
	raw, err := os.ReadFile(filepath.Join(appbuild.AppArtifactRoot(q.Root, q.App), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build wrote no %s; rebuild the app", q.App, edge.RoutingManifestFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the routing manifest %s routes by: %w", q.App, err)
	}
	return &provider.RoutingSpec{Entry: desc.Entry, Manifest: raw}, nil
}

func withoutSlash(prefix string) string {
	return strings.TrimSuffix(prefix, naming.PathSeparator)
}

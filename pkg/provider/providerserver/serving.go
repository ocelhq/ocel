package providerserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type AppServingInput struct {
	Root              string
	Project           string
	App               string
	Framework         string
	Compute           provider.Compute
	Stack             naming.StackName
	Coordinate        naming.Coordinate
	EdgeRunsCode      bool
	EdgeSignsForwards bool
}

type AppServing struct {
	Entry          string
	OriginDispatch *provider.RoutingSpec
	EdgeDispatch   *provider.RoutingSpec
	Guard          *provider.OriginGuard
	ISR            *provider.ISRSpec
	Bytecode       *provider.BytecodeSpec
	AssetPrefix    string
}

func AppServingFor(q AppServingInput) (AppServing, error) {
	hosting, present, err := buildoutput.ReadHosting(q.Root, q.App)
	if err != nil {
		return AppServing{}, err
	}
	facts := AppServing{
		AssetPrefix: q.Coordinate.AssetKey(""),
		Bytecode:    &provider.BytecodeSpec{Prefix: withoutSlash(q.Coordinate.BytecodePrefix())},
	}
	if present {
		facts.Entry = hosting.Entry
	}
	if q.Framework == buildoutput.FrameworkNext {
		facts.ISR = &provider.ISRSpec{
			Prefix:       withoutSlash(q.Coordinate.ISRPrefix()),
			TagNamespace: naming.ISRTagPrefix(q.Project, q.Stack),
		}
	}
	if q.Compute != provider.ComputeContainer {
		routing, err := routingFor(q, hosting, present)
		if err != nil {
			return AppServing{}, err
		}
		if q.EdgeRunsCode {
			facts.EdgeDispatch = routing
		} else {
			facts.OriginDispatch = routing
		}
	}
	facts.Guard = guardFor(q, hosting, present)
	return facts, nil
}

func guardFor(q AppServingInput, hosting buildoutput.Hosting, present bool) *provider.OriginGuard {
	if q.EdgeRunsCode || q.EdgeSignsForwards || !present || hosting.Entry == "" {
		return nil
	}
	return &provider.OriginGuard{Entry: hosting.Entry}
}

func anyProxied(proxied func(provider.BindingType) bool, grants []provider.Binding) bool {
	return slices.ContainsFunc(grants, func(binding provider.Binding) bool { return proxied(binding.Type) })
}

func routingFor(q AppServingInput, hosting buildoutput.Hosting, present bool) (*provider.RoutingSpec, error) {
	if !present || !hosting.EdgeRouting {
		return nil, nil
	}
	if hosting.Entry == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build names no entry route; rebuild the app", q.App)
	}
	raw, err := os.ReadFile(filepath.Join(buildoutput.AppRoot(q.Root, q.App), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build wrote no %s; rebuild the app", q.App, edge.RoutingManifestFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the routing manifest %s routes by: %w", q.App, err)
	}
	return &provider.RoutingSpec{Entry: hosting.Entry, Manifest: raw}, nil
}

func withoutSlash(prefix string) string {
	return strings.TrimSuffix(prefix, naming.PathSeparator)
}

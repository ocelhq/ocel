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
	"github.com/ocelhq/ocel/pkg/router"
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
	RootFunction   string
	OriginDispatch *provider.RoutingSpec
	EdgeDispatch   *provider.RoutingSpec
	Guard          *provider.OriginGuard
	ISR            *provider.ISRSpec
	Bytecode       *provider.BytecodeSpec
	AssetPrefix    string
	Static         *buildoutput.Static
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
		facts.RootFunction = hosting.RootFunction
		facts.Static = hosting.Static
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
	if q.EdgeRunsCode || q.EdgeSignsForwards || !present || hosting.RootFunction == "" {
		return nil
	}
	return &provider.OriginGuard{RootFunction: hosting.RootFunction}
}

func anyProxied(proxied func(provider.BindingType) bool, grants []provider.Binding) bool {
	return slices.ContainsFunc(grants, func(binding provider.Binding) bool { return proxied(binding.Type) })
}

func routingFor(q AppServingInput, hosting buildoutput.Hosting, present bool) (*provider.RoutingSpec, error) {
	if !present || hosting.RouteTable == "" {
		return nil, nil
	}
	file, known := routeTableFiles[hosting.RouteTable]
	if !known {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s routes by a %q route table, which no router this CLI ships reads; rebuild the app with this CLI", q.App, hosting.RouteTable)
	}
	if hosting.RootFunction == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s routes by a route table but its build names no root function; rebuild the app", q.App)
	}
	raw, err := os.ReadFile(filepath.Join(buildoutput.AppRoot(q.Root, q.App), file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s routes by a route table but its build wrote no %s; rebuild the app", q.App, file)
	}
	if err != nil {
		return nil, fmt.Errorf("read the route table %s routes by: %w", q.App, err)
	}
	return &provider.RoutingSpec{
		RootFunction: hosting.RootFunction,
		RouteTable:   router.RouteTable{Format: hosting.RouteTable, Table: raw},
	}, nil
}

var routeTableFiles = map[buildoutput.RouteTableFormat]string{
	buildoutput.RouteTableNext: edge.NextRouteTableFile,
}

func withoutSlash(prefix string) string {
	return strings.TrimSuffix(prefix, naming.PathSeparator)
}

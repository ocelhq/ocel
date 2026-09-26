package providerkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	FrameworkNode = "node"
	FrameworkNext = "next"
	FrameworkGo   = "go"

	FrameworkPython = "python"

	FrameworkRust = "rust"
)

func Frameworks() []string {
	return []string{FrameworkNode, FrameworkNext, FrameworkGo, FrameworkPython, FrameworkRust}
}

func KnownFramework(name string) bool { return slices.Contains(Frameworks(), name) }

const (
	ArchX8664 = "x86_64"
	ArchARM64 = "arm64"
)

func Architecture(declared string) string {
	if declared == "" {
		return ArchX8664
	}
	return declared
}

func GoArch(declared string) (string, bool) {
	switch Architecture(declared) {
	case ArchX8664:
		return "amd64", true
	case ArchARM64:
		return "arm64", true
	}
	return "", false
}

const (
	NodePackageOS   = "linux"
	NodePackageLibc = "glibc"
)

func NodePackageCPU(declared string) (string, bool) {
	switch Architecture(declared) {
	case ArchX8664:
		return "x64", true
	case ArchARM64:
		return "arm64", true
	}
	return "", false
}

func RustTarget(declared string) (string, bool) {
	switch Architecture(declared) {
	case ArchX8664:
		return "x86_64-unknown-linux-musl", true
	case ArchARM64:
		return "aarch64-unknown-linux-musl", true
	}
	return "", false
}

const PythonVersion = "3.13"

func PythonPlatformTag(declared string) (string, bool) {
	switch Architecture(declared) {
	case ArchX8664:
		return "manylinux2014_x86_64", true
	case ArchARM64:
		return "manylinux2014_aarch64", true
	}
	return "", false
}

const (
	elfMachineX8664 = 0x3E
	elfMachineARM64 = 0xB7
)

func ELFMachine(declared string) (uint16, bool) {
	switch Architecture(declared) {
	case ArchX8664:
		return elfMachineX8664, true
	case ArchARM64:
		return elfMachineARM64, true
	}
	return 0, false
}

func ArchOfELFMachine(machine uint16) (string, bool) {
	switch machine {
	case elfMachineX8664:
		return ArchX8664, true
	case elfMachineARM64:
		return ArchARM64, true
	}
	return "", false
}

type ServingQuery struct {
	Root              string
	Project           string
	App               string
	Framework         string
	Stack             naming.StackName
	Coordinate        naming.Coordinate
	EdgeRunsCode      bool
	EdgeSignsForwards bool
}

type ServingFacts struct {
	Entry       string
	Routing     *RoutingPlan
	EdgeRouting *RoutingPlan
	Guard       *OriginGuard
	ISR         *ISRPlan
	Bytecode    *BytecodePlan
	AssetPrefix string
}

func ServingFactsFor(q ServingQuery) (ServingFacts, error) {
	desc, present, err := ReadServeDescriptor(q.Root, q.App)
	if err != nil {
		return ServingFacts{}, err
	}
	facts := ServingFacts{
		AssetPrefix: q.Coordinate.AssetKey(""),
		Bytecode:    &BytecodePlan{Prefix: withoutSlash(q.Coordinate.BytecodePrefix())},
	}
	if present {
		facts.Entry = desc.Entry
	}
	if q.Framework == FrameworkNext {
		facts.ISR = &ISRPlan{
			Prefix:       withoutSlash(q.Coordinate.ISRPrefix()),
			TagNamespace: naming.ISRTagPrefix(q.Project, q.Stack),
		}
	}
	routing, err := routingFor(q, desc, present)
	if err != nil {
		return ServingFacts{}, err
	}
	if q.EdgeRunsCode {
		facts.EdgeRouting = routing
	} else {
		facts.Routing = routing
	}
	facts.Guard = guardFor(q, desc, present)
	return facts, nil
}

func guardFor(q ServingQuery, desc edge.ServeDescriptor, present bool) *OriginGuard {
	if q.EdgeRunsCode || q.EdgeSignsForwards || !present || desc.Entry == "" {
		return nil
	}
	return &OriginGuard{Entry: desc.Entry}
}

func anyProxied(proxied func(BindingType) bool, grants []Binding) bool {
	return slices.ContainsFunc(grants, func(binding Binding) bool { return proxied(binding.Type) })
}

func routingFor(q ServingQuery, desc edge.ServeDescriptor, present bool) (*RoutingPlan, error) {
	if !present || !desc.EdgeRouting {
		return nil, nil
	}
	if desc.Entry == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build names no entry route; rebuild the app", q.App)
	}
	raw, err := os.ReadFile(filepath.Join(AppArtifactRoot(q.Root, q.App), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s declares edge routing but its build wrote no %s; rebuild the app", q.App, edge.RoutingManifestFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the routing manifest %s routes by: %w", q.App, err)
	}
	return &RoutingPlan{Entry: desc.Entry, Manifest: raw}, nil
}

func withoutSlash(prefix string) string {
	return strings.TrimSuffix(prefix, naming.PathSeparator)
}

package appbuild

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/constants"
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

const ClientURLEnvName = "NEXT_PUBLIC_OCEL_URL"

func FrameworkBundlesClient(framework string) bool {
	return framework == FrameworkNode || framework == FrameworkNext
}

func IsOcelInjectedEnv(clientBundle bool, key string) bool {
	switch key {
	case constants.AppURLEnvName:
		return true
	case ClientURLEnvName:
		return clientBundle
	}
	return false
}

type Framework struct {
	Name string `json:"name"`
	Arch string `json:"arch,omitempty"`
}

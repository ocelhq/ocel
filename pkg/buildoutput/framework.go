package buildoutput

import "slices"

const (
	FrameworkNode      = "node"
	FrameworkNext      = "next"
	FrameworkSvelteKit = "sveltekit"
	FrameworkGo        = "go"

	FrameworkPython = "python"

	FrameworkRust = "rust"
)

func Frameworks() []string {
	return []string{FrameworkNode, FrameworkNext, FrameworkSvelteKit, FrameworkGo, FrameworkPython, FrameworkRust}
}

func IsKnownFramework(name string) bool { return slices.Contains(Frameworks(), name) }

func RunsOnNode(framework string) bool {
	return framework == FrameworkNode || framework == FrameworkNext || framework == FrameworkSvelteKit
}

func BuildsWithItsOwnScript(framework string) bool {
	return framework == FrameworkNext || framework == FrameworkSvelteKit
}

type Framework struct {
	Name string `json:"name"`
	Arch string `json:"arch,omitempty"`
}

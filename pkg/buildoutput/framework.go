package buildoutput

import "slices"

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

func IsKnownFramework(name string) bool { return slices.Contains(Frameworks(), name) }

func FrameworkBundlesClient(framework string) bool {
	return framework == FrameworkNode || framework == FrameworkNext
}

type Framework struct {
	Name string `json:"name"`
	Arch string `json:"arch,omitempty"`
}

package language

import "os"

type Language string

const (
	JS     Language = "js"
	Go     Language = "go"
	Python Language = "python"
	Rust   Language = "rust"
)

func ManifestNames() []string {
	var names []string
	for _, m := range manifests {
		names = append(names, m.files...)
	}
	return names
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

package naming

import "strings"

func (c Coordinate) StoragePrefix() string {
	return path(c.Env, c.Project, c.App, c.Release.String()) + PathSeparator
}

func (c Coordinate) FunctionArtifactKey(sha string) string {
	return c.StoragePrefix() + path(string(KindFunction), Sanitize(c.Name), sha+".zip")
}

func (c Coordinate) AssetKey(assetPath string) string {
	return c.StoragePrefix() + path("assets", strings.TrimPrefix(assetPath, PathSeparator))
}

func (c Coordinate) ImageConfigKey() string {
	return c.StoragePrefix() + "image-config.json"
}

func (c Coordinate) ISRPrefix() string {
	return c.StoragePrefix() + "isr" + PathSeparator
}

func (c Coordinate) BytecodePrefix() string {
	return c.StoragePrefix() + "bytecode" + PathSeparator
}

func path(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, PathSeparator)
}

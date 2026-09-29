package toolchain

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

var emittedExtensions = map[string]string{
	".ts":  ".js",
	".tsx": ".js",
	".mts": ".mjs",
	".cts": ".cjs",
}

func (t Target) TracedHandler() (string, error) {
	rel, err := filepath.Rel(t.Source, t.Entrypoint)
	if err != nil || !filepath.IsLocal(rel) || slices.Contains(strings.Split(rel, string(filepath.Separator)), nodeModulesDir) {
		return "", fmt.Errorf("entrypoint %s for app %q is not one of the app's own sources under %s, and a traced app is served from the entrypoint it copies there; name an entrypoint inside the app, or unset %s to bundle it",
			t.Entrypoint, t.App, t.Source, PreferTracingEnv)
	}
	ext := filepath.Ext(rel)
	if emitted, ok := emittedExtensions[ext]; ok {
		rel = strings.TrimSuffix(rel, ext) + emitted
	}
	return filepath.ToSlash(rel), nil
}

func DescribeTrace(t Target) error {
	if err := refuseUnstated("describe a trace", []statedField{
		{"app", t.App},
		{"appDir", t.AppDir},
		{"framework", t.Framework.Name},
		{"funcDir", t.FuncDir},
	}); err != nil {
		return err
	}
	handler, err := t.TracedHandler()
	if err != nil {
		return err
	}
	return describeArtifact(t.App, t.Framework, handler, nil, t.FuncDir, t.AppDir)
}

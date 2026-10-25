package toolchain

import (
	"context"
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
		return "", fmt.Errorf("entrypoint %s for app %q is outside the app's sources under %s: name an entrypoint inside the app, or unset %s to bundle it",
			t.Entrypoint, t.App, t.Source, PreferTracingEnv)
	}
	ext := filepath.Ext(rel)
	if emitted, ok := emittedExtensions[ext]; ok {
		rel = strings.TrimSuffix(rel, ext) + emitted
	}
	return filepath.ToSlash(rel), nil
}

func DescribeTrace(ctx context.Context, t Target) error {
	if err := refuseUnstated("describe a trace", []statedField{
		{"app", t.App},
		{"appDir", t.AppDir},
		{"framework", t.Framework.Name},
		{"functionDir", t.FunctionDir},
	}); err != nil {
		return err
	}
	handler, err := t.TracedHandler()
	if err != nil {
		return err
	}
	if err := describeArtifact(t.App, t.Framework, handler, nil, nil, t.FunctionDir, t.AppDir); err != nil {
		return err
	}
	return BundleWorker(ctx, t)
}

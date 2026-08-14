package appbundler

import (
	"bytes"
	"strings"
	"testing"
)

func bundleLog(t *testing.T, files tree, entry string) string {
	t.Helper()
	l := newLayout(t, files)
	target := l.target(entry)
	var log bytes.Buffer
	target.Log = &log
	if err := Bundle(target); err != nil {
		t.Fatal(err)
	}
	return log.String()
}

func TestBundleWarnsAboutStaticDirectories(t *testing.T) {
	t.Parallel()
	log := bundleLog(t, tree{
		"package.json": appPkg,
		"index.js":     "const app = {use(){}, static(){}};\napp.use(app.static('./public'));\n",
	}, "index.js")

	if !strings.Contains(log, "a directory served as static files") {
		t.Fatalf("no static-directory warning:\n%s", log)
	}
	if !strings.Contains(log, "OCEL_BUILD_PREFER_TRACING=1") {
		t.Fatalf("warning omits the escape hatch:\n%s", log)
	}
}

func TestBundleWarnsAboutViewsAndSourceRelativeReads(t *testing.T) {
	t.Parallel()
	log := bundleLog(t, tree{
		"package.json": appPkg,
		"index.js":     "export * from './routes.js';\n",
		"routes.js":    "import {readFileSync} from 'node:fs';\nexport const q = readFileSync(__dirname + '/q.sql');\nexport const v = 'views';\n",
	}, "index.js")

	if !strings.Contains(log, "routes.js wants") {
		t.Fatalf("no warning for the imported source:\n%s", log)
	}
	if !strings.Contains(log, "view templates") || !strings.Contains(log, "relative to their own source") {
		t.Fatalf("warning misses a signal:\n%s", log)
	}
}

func TestBundleStaysQuietWithoutRuntimeFiles(t *testing.T) {
	t.Parallel()
	log := bundleLog(t, tree{
		"package.json": appPkg,
		"index.js":     "export const handler = () => 'ok';\n",
	}, "index.js")

	if log != "" {
		t.Fatalf("unexpected warning:\n%s", log)
	}
}

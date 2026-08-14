package appbundler

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const runtimeFileScanLimit = 512 << 10

var runtimeFileSignals = []struct {
	pattern *regexp.Regexp
	reads   string
}{
	{regexp.MustCompile(`\.static\s*\(`), "a directory served as static files"},
	{regexp.MustCompile(`\.render\s*\(|["'` + "`" + `]views["'` + "`" + `]`), "view templates"},
	{regexp.MustCompile(`\b__dirname\b|\b__filename\b`), "files addressed relative to their own source"},
}

func reportRuntimeFileRisk(log io.Writer, app, metafile, root string) {
	if log == nil {
		return
	}
	found := scanRuntimeFileRisk(metafile, root)
	if len(found) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ocel: bundling %s ships only what it can import, and these sources reach for more:\n", app)
	for _, line := range found {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	fmt.Fprintf(&b, "copy them into the artifact yourself, or %s\n", tracingHint)
	io.WriteString(log, b.String())
}

func scanRuntimeFileRisk(metafile, root string) []string {
	var meta struct {
		Inputs map[string]struct{} `json:"inputs"`
	}
	if json.Unmarshal([]byte(metafile), &meta) != nil {
		return nil
	}

	paths := make([]string, 0, len(meta.Inputs))
	for input := range meta.Inputs {
		if strings.Contains(input, nodeModulesDirName+"/") {
			continue
		}
		paths = append(paths, input)
	}
	sort.Strings(paths)

	var found []string
	for _, input := range paths {
		reads := runtimeFileReads(filepath.Join(root, filepath.FromSlash(input)))
		if len(reads) > 0 {
			found = append(found, fmt.Sprintf("%s wants %s", input, strings.Join(reads, ", ")))
		}
	}
	return found
}

func runtimeFileReads(path string) []string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > runtimeFileScanLimit {
		return nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reads []string
	for _, signal := range runtimeFileSignals {
		if signal.pattern.Match(source) {
			reads = append(reads, signal.reads)
		}
	}
	return reads
}

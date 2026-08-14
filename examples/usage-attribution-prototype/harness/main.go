package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const beaconPrefix = "@@OCEL-BEACON@@"

type beaconRecord struct {
	Type  string `json:"type"`
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Stack string `json:"stack"`
}

type attribution map[string]map[string][]string

var fixtureRoot string

func main() {
	var err error
	fixtureRoot, err = filepath.Abs("..")
	if err != nil {
		fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(fixtureRoot, "sdk")); statErr != nil {
		fatal(fmt.Errorf("run from harness/ inside the prototype dir: %w", statErr))
	}

	apps := map[string][]string{
		"api":    discover(filepath.Join(fixtureRoot, "apps", "api")),
		"worker": discover(filepath.Join(fixtureRoot, "apps", "worker")),
	}

	fmt.Println("=== ground truth ===")
	printAttribution(groundTruth())

	fmt.Println("\n=== mode 1: beacons, single process (today's discovery shape) ===")
	single := beaconSingle(apps)
	printAttribution(single)
	diff(groundTruth(), single)

	fmt.Println("\n=== mode 2: beacons, one process per app ===")
	perApp := beaconPerApp(apps)
	printAttribution(perApp)
	diff(groundTruth(), perApp)

	fmt.Println("\n=== mode 3: esbuild import scan, no execution ===")
	scanned := scan(apps)
	printAttribution(scanned)
	diff(groundTruth(), scanned)

	fmt.Println("\n=== mode 4: hybrid (scan graph + beacon ids) ===")
	hybrid := hybridMode(apps)
	printAttribution(hybrid)
	diff(groundTruth(), hybrid)

	fmt.Println("\n=== mode 5: hybrid + tree-shaking (plugin marks user modules side-effect-free) ===")
	shaken := treeShakeHybrid(apps)
	printAttribution(shaken)
	diff(groundTruth(), shaken)
}

func treeShakeHybrid(apps map[string][]string) attribution {
	var all []string
	for _, files := range apps {
		all = append(all, files...)
	}
	slices.Sort(all)
	declaringFile := map[string]string{}
	for _, rec := range bundleAndRun("entry-shaken", all) {
		for _, f := range stackFiles(rec.Stack) {
			if !strings.HasPrefix(f, "sdk/") {
				declaringFile[key(rec)] = f
				break
			}
		}
	}

	out := attribution{}
	for _, app := range sortedKeys(apps) {
		for entry, survivors := range shakenSurvivors(app, apps[app]) {
			for res, file := range declaringFile {
				if survivors[file] {
					addTo(out, app, res, entry)
				}
			}
		}
	}
	return out
}

func shakenSurvivors(app string, files []string) map[string]map[string]bool {
	pureUserModules := api.Plugin{
		Name: "pure-user-modules",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.PluginData == "resolving" || args.Importer == "" {
					return api.OnResolveResult{}, nil
				}
				r := build.Resolve(args.Path, api.ResolveOptions{
					Importer:   args.Importer,
					ResolveDir: args.ResolveDir,
					Kind:       args.Kind,
					PluginData: "resolving",
				})
				if len(r.Errors) > 0 || r.External {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: r.Path, SideEffects: api.SideEffectsFalse}, nil
			})
		},
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:   files,
		AbsWorkingDir: fixtureRoot,
		Bundle:        true,
		Platform:      api.PlatformNode,
		Format:        api.FormatESModule,
		Outdir:        filepath.Join(fixtureRoot, ".ocel-proto", "shake-"+app),
		Write:         false,
		Metafile:      true,
		Plugins:       []api.Plugin{pureUserModules},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		fatal(fmt.Errorf("shake %s:\n%s", app, strings.Join(msgs, "\n")))
	}

	var meta struct {
		Outputs map[string]struct {
			EntryPoint string `json:"entryPoint"`
			Inputs     map[string]struct {
				BytesInOutput int `json:"bytesInOutput"`
			} `json:"inputs"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		fatal(err)
	}

	out := map[string]map[string]bool{}
	for _, data := range meta.Outputs {
		if data.EntryPoint == "" {
			continue
		}
		survivors := map[string]bool{}
		for input, usage := range data.Inputs {
			if usage.BytesInOutput > 0 {
				survivors[input] = true
			}
		}
		out[data.EntryPoint] = survivors
	}
	return out
}

func groundTruth() attribution {
	return attribution{
		"api": {
			"postgres:main-db":   {"apps/api/src/reports.ts", "apps/api/src/server.ts"},
			"postgres:tenant-db": {"apps/api/src/server.ts"},
			"bucket:uploads":     {"apps/api/src/server.ts"},
		},
		"worker": {
			"postgres:main-db":      {"apps/worker/src/worker.ts"},
			"postgres:analytics-db": {"apps/worker/src/jobs.ts"},
			"postgres:metrics-db":   {"apps/worker/src/worker.ts"},
		},
	}
}

func discover(root string) []string {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".ts") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fatal(err)
	}
	slices.Sort(files)
	return files
}

func bundleAndRun(name string, files []string) []beaconRecord {
	outDir := filepath.Join(fixtureRoot, ".ocel-proto")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	outfile := filepath.Join(outDir, name+".mjs")

	var entry strings.Builder
	for _, f := range files {
		fmt.Fprintf(&entry, "await import(%q);\n", f)
	}

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry.String(),
			ResolveDir: fixtureRoot,
			Sourcefile: "proto-discovery-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:    true,
		Platform:  api.PlatformNode,
		Sourcemap: api.SourceMapInline,
		Format:    api.FormatESModule,
		Outfile:   outfile,
		Write:     true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		fatal(fmt.Errorf("bundle %s:\n%s", name, strings.Join(msgs, "\n")))
	}

	cmd := exec.Command("node", "--enable-source-maps", outfile)
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery")
	out, err := cmd.Output()
	if err != nil {
		fatal(fmt.Errorf("node %s: %w\n%s", name, err, out))
	}

	var records []beaconRecord
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, beaconPrefix) {
			continue
		}
		var rec beaconRecord
		if err := json.Unmarshal([]byte(line[len(beaconPrefix):]), &rec); err != nil {
			fatal(err)
		}
		records = append(records, rec)
	}
	return records
}

var frameRe = regexp.MustCompile(`\(?((?:/|file:///)[^):\s]+\.(?:ts|mjs|js)):(\d+):(\d+)\)?`)

func stackFiles(stack string) []string {
	var out []string
	for _, m := range frameRe.FindAllStringSubmatch(stack, -1) {
		p := strings.TrimPrefix(m[1], "file://")
		if rel, err := filepath.Rel(fixtureRoot, p); err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, rel)
		}
	}
	return out
}

func appOfFile(rel string) string {
	switch {
	case strings.HasPrefix(rel, "apps/api/"):
		return "api"
	case strings.HasPrefix(rel, "apps/worker/"):
		return "worker"
	}
	return ""
}

func key(rec beaconRecord) string { return rec.Kind + ":" + rec.ID }

func beaconSingle(apps map[string][]string) attribution {
	var all []string
	for _, files := range apps {
		all = append(all, files...)
	}
	slices.Sort(all)
	records := bundleAndRun("entry-single", all)

	out := attribution{}
	for _, rec := range records {
		frames := stackFiles(rec.Stack)
		fmt.Printf("  beacon %-22s frames: %v\n", key(rec), frames)
		attributed := false
		for _, f := range frames {
			if app := appOfFile(f); app != "" {
				addTo(out, app, key(rec), f)
				attributed = true
			}
		}
		if !attributed {
			addTo(out, "<unattributable>", key(rec), firstOr(frames, "<no frames>"))
		}
	}
	return out
}

func beaconPerApp(apps map[string][]string) attribution {
	out := attribution{}
	for _, app := range sortedKeys(apps) {
		for _, rec := range bundleAndRun("entry-"+app, apps[app]) {
			frames := stackFiles(rec.Stack)
			file := "<declared outside app>"
			for _, f := range frames {
				if appOfFile(f) == app {
					file = f
					break
				}
			}
			if file == "<declared outside app>" && len(frames) > 0 {
				file = frames[len(frames)-1] + " (declaring file; importer unknown)"
			}
			addTo(out, app, key(rec), file)
		}
	}
	return out
}

type metafile struct {
	Inputs map[string]struct {
		Imports []struct {
			Path     string `json:"path"`
			Kind     string `json:"kind"`
			External bool   `json:"external"`
		} `json:"imports"`
	} `json:"inputs"`
}

var callRe = regexp.MustCompile(`(postgres|bucket)\(\s*(?:"([^"]*)"|[^)])`)

func scan(apps map[string][]string) attribution {
	out := attribution{}
	for _, app := range sortedKeys(apps) {
		graph := importGraph(app, apps[app])
		for _, entry := range apps[app] {
			rel, _ := filepath.Rel(fixtureRoot, entry)
			for _, reached := range reachable(graph, rel) {
				if strings.HasPrefix(reached, "sdk/") {
					continue
				}
				for _, res := range declaredResources(reached) {
					addTo(out, app, res, rel)
				}
			}
		}
	}
	return out
}

func importGraph(app string, files []string) map[string][]string {
	result := api.Build(api.BuildOptions{
		EntryPoints:   files,
		AbsWorkingDir: fixtureRoot,
		Bundle:        true,
		Platform:      api.PlatformNode,
		Format:        api.FormatESModule,
		Outdir:        filepath.Join(fixtureRoot, ".ocel-proto", "scan-"+app),
		Write:         false,
		Metafile:      true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		fatal(fmt.Errorf("scan %s:\n%s", app, strings.Join(msgs, "\n")))
	}
	for _, w := range result.Warnings {
		fmt.Printf("  esbuild warning [%s]: %s\n", app, w.Text)
	}

	var meta metafile
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		fatal(err)
	}
	graph := map[string][]string{}
	for input, data := range meta.Inputs {
		for _, imp := range data.Imports {
			if imp.External {
				continue
			}
			graph[input] = append(graph[input], imp.Path)
		}
	}
	return graph
}

func reachable(graph map[string][]string, from string) []string {
	seen := map[string]bool{from: true}
	queue := []string{from}
	var order []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, next := range graph[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return order
}

func declaredResources(rel string) []string {
	src, err := os.ReadFile(filepath.Join(fixtureRoot, rel))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range callRe.FindAllStringSubmatch(string(src), -1) {
		if strings.Contains(rel, "sdk/") {
			continue
		}
		id := m[2]
		if id == "" {
			id = "<unresolved id>"
		}
		out = append(out, m[1]+":"+id)
	}
	return out
}

func hybridMode(apps map[string][]string) attribution {
	var all []string
	for _, files := range apps {
		all = append(all, files...)
	}
	slices.Sort(all)
	declaringFile := map[string]string{}
	for _, rec := range bundleAndRun("entry-hybrid", all) {
		for _, f := range stackFiles(rec.Stack) {
			if !strings.HasPrefix(f, "sdk/") {
				declaringFile[key(rec)] = f
				break
			}
		}
	}

	out := attribution{}
	for _, app := range sortedKeys(apps) {
		graph := importGraph(app, apps[app])
		for _, entry := range apps[app] {
			rel, _ := filepath.Rel(fixtureRoot, entry)
			reached := map[string]bool{}
			for _, r := range reachable(graph, rel) {
				reached[r] = true
			}
			for res, file := range declaringFile {
				if reached[file] {
					addTo(out, app, res, rel)
				}
			}
		}
	}
	return out
}

func addTo(a attribution, app, resource, file string) {
	if a[app] == nil {
		a[app] = map[string][]string{}
	}
	if !slices.Contains(a[app][resource], file) {
		a[app][resource] = append(a[app][resource], file)
		slices.Sort(a[app][resource])
	}
}

func printAttribution(a attribution) {
	for _, app := range sortedKeys(a) {
		fmt.Printf("  %s:\n", app)
		for _, res := range sortedKeys(a[app]) {
			fmt.Printf("    %-24s %s\n", res, strings.Join(a[app][res], ", "))
		}
	}
}

func diff(want, got attribution) {
	var lines []string
	for _, app := range sortedKeys(want) {
		for res := range want[app] {
			if _, ok := got[app][res]; !ok {
				lines = append(lines, fmt.Sprintf("  MISSING  %s -> %s", app, res))
			}
		}
	}
	for _, app := range sortedKeys(got) {
		if app == "<unattributable>" {
			for res := range got[app] {
				lines = append(lines, fmt.Sprintf("  UNATTRIBUTED  %s", res))
			}
			continue
		}
		for res := range got[app] {
			if _, ok := want[app][res]; !ok {
				lines = append(lines, fmt.Sprintf("  EXTRA    %s -> %s", app, res))
			}
		}
	}
	if len(lines) == 0 {
		fmt.Println("  verdict: matches ground truth")
		return
	}
	sort.Strings(lines)
	fmt.Println("  verdict:")
	for _, l := range lines {
		fmt.Println(l)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstOr(s []string, fallback string) string {
	if len(s) == 0 {
		return fallback
	}
	return s[0]
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

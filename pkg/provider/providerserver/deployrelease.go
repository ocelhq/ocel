package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func findOwnAddresses(entry provider.AppEntry, facts AppServing, result provider.StackResult) (address, deployment string) {
	if container, found := findOwnContainer(result.Containers, entry.App); found {
		return container.URL, container.DeploymentURL
	}
	function := findOwnFunction(entry, facts, result.Functions)
	return function.URL, function.DeploymentURL
}

func findOwnContainer(containers []provider.AppContainer, app string) (provider.AppContainer, bool) {
	for _, container := range containers {
		if container.Name == app && container.URL != "" {
			return container, true
		}
	}
	return provider.AppContainer{}, false
}

func findOwnFunction(entry provider.AppEntry, facts AppServing, functions []provider.Function) provider.Function {
	named := rootFunctionLogicalName(entry.Manifest, facts.RootFunction)
	if declared := provider.ListFunctions(entry.Manifest); named == "" && len(declared) == 1 {
		named = declared[0].GetLogicalName()
	}
	for _, fn := range functions {
		if named != "" && fn.Name == named {
			return fn
		}
	}
	return provider.Function{}
}

func rootFunctionLogicalName(app *contractv1.ManifestApp, rootFunction string) string {
	if rootFunction == "" {
		return ""
	}
	for _, fn := range provider.ListFunctions(app) {
		if resolveRouteID(fn) == rootFunction {
			return fn.GetLogicalName()
		}
	}
	return ""
}

func declaredVariables(framework string, values provider.AppValues) []router.VariableRecord {
	names := make([]string, 0, len(values.Plain)+len(values.Sensitive)+len(values.Secrets))
	for _, key := range slices.Sorted(maps.Keys(values.Plain)) {
		if !processenv.IsInjected(framework, key) {
			names = append(names, key)
		}
	}
	names = append(names, slices.Sorted(maps.Keys(values.Sensitive))...)
	folders := make(map[string]string, len(values.Secrets))
	for _, secret := range values.Secrets {
		names = append(names, secret.Key)
		folders[secret.Key] = secret.Folder
	}
	slices.Sort(names)
	declared := make([]router.VariableRecord, 0, len(names))
	for _, name := range names {
		declared = append(declared, router.VariableRecord{Key: name, Folder: folders[name]})
	}
	return declared
}

func (r *deployRun) recordStagedRelease(ctx context.Context, release appStack, result provider.StackResult) error {
	entry, facts, images, values := release.entry, release.facts, release.images, release.values
	urlByLogical := make(map[string]string, len(result.Functions))
	physicalByLogical := make(map[string]string, len(result.Functions))
	for _, fn := range result.Functions {
		urlByLogical[fn.Name] = fn.URL
		physicalByLogical[fn.Name] = fn.Physical
	}
	if entry.IsBuiltFromFunction() {
		for _, fn := range provider.ListFunctions(entry.Manifest) {
			urlByLogical[fn.GetLogicalName()] = originOf(result.Containers, entry.App)
			physicalByLogical[fn.GetLogicalName()] = physicalOf(result.Containers, entry.App)
		}
	}
	urls := make(map[string]string, len(result.Functions))
	var logical []string
	for _, fn := range provider.ListFunctions(entry.Manifest) {
		logical = append(logical, fn.GetLogicalName())
		if url := urlByLogical[fn.GetLogicalName()]; url != "" {
			urls[resolveRouteID(fn)] = url
		}
	}
	coordinate := appCoordinate(r.spec, entry.App, entry.Release.Token())
	var routeTable *router.RouteTableLocation
	origin := originOf(result.Containers, entry.App)
	if facts.EdgeRouteTable != nil {
		if result.RouteTableKey != facts.EdgeRouteTable.Location.Key {
			return refusal.Refuse(refusal.CodeNotReady,
				"%s is routed at %s by its route table, and this provider stored none where the edge reads it. Re-run `%s` so this account adopts the edge's cache store",
				entry.App, describeFront(r.front.Kind()), provider.BootstrapCommand(r.spec.Tier))
		}
		routeTable = &facts.EdgeRouteTable.Location
		if origin == "" {
			origin = urlByLogical[rootFunctionLogicalName(entry.Manifest, facts.RootFunction)]
		}
	}
	record := router.ReleaseRecord{
		RouteTable:              routeTable,
		Static:                  facts.Static,
		App:                     entry.App,
		Environment:             r.spec.Env,
		Framework:               entry.Manifest.GetFramework().GetName(),
		Release:                 r.spec.Releases[entry.App],
		BuildID:                 entry.Release.BuildID(),
		RootFunction:            facts.RootFunction,
		RootFunctionPhysical:    physicalByLogical[rootFunctionLogicalName(entry.Manifest, facts.RootFunction)],
		Image:                   images.ImageRef(entry.App),
		Physical:                physicalOf(result.Containers, entry.App),
		Revisions:               revisionsOf(result, entry.App, logical),
		Origin:                  origin,
		HealthPath:              healthPathOf(entry, result.Containers),
		HealthPathDiscovered:    entry.HealthCheckPath == "" && discoveredHealthPathOf(result.Containers, entry.App) != "",
		FunctionURLs:            urls,
		StorageCoordinate:       coordinate.StorageCoordinate(),
		Prerenders:              facts.Prerenders,
		Secrets:                 router.ReleaseSecrets{PurgeSecret: result.PurgeSecret},
		CreatedAt:               release.createdAt,
		ReleaseFingerprint:      entry.Release.Fingerprint(),
		OriginSecretFingerprint: result.OriginSecretFingerprint,
		Variables:               declaredVariables(entry.Manifest.GetFramework().GetName(), values),
		Needs:                   r.needs[entry.App].Needs,
		SupportInEffect:         r.needs[entry.App].InEffect,
		Waived:                  r.needs[entry.App].Waived,
	}
	code, err := r.edgeCode(entry, result)
	if err != nil {
		return err
	}
	if code != nil {
		record.EdgeBundle = code
		record.Secrets.Envelope = result.Envelope
		if env := variableEnv(values); len(env) > 0 {
			record.Secrets.Env = env
		}
	}
	return r.ledger.putStaged(ctx, record)
}

func (r *deployRun) edgeCode(entry provider.AppEntry, result provider.StackResult) (*router.EdgeBundle, error) {
	if result.EdgeBundleKey == "" || !r.front.Facts().RunsCode {
		return nil, nil
	}
	bundle, err := os.ReadFile(filepath.Join(buildoutput.AppRoot(r.artifactRoot, entry.App), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s was released with an edge bundle at %s but its build left no %s for the edge to load; rebuild the app",
			entry.App, result.EdgeBundleKey, edge.AppBundleFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the edge bundle %s runs: %w", entry.App, err)
	}
	digest := sha256.Sum256(bundle)
	return &router.EdgeBundle{Key: result.EdgeBundleKey, Digest: hex.EncodeToString(digest[:])}, nil
}

func variableEnv(values provider.AppValues) map[string]string {
	env := make(map[string]string, len(values.Plain)+1)
	maps.Copy(env, values.Plain)
	if values.Folder != "" {
		env[processenv.AppFolderEnvVar] = values.Folder
	}
	return env
}

func revisionsOf(result provider.StackResult, app string, logical []string) map[string]string {
	revisions := make(map[string]string, len(logical)+1)
	for _, container := range result.Containers {
		if container.Name == app && container.Physical != "" && container.Revision != "" {
			revisions[container.Physical] = container.Revision
		}
	}
	for _, fn := range result.Functions {
		if slices.Contains(logical, fn.Name) && fn.Physical != "" && fn.Revision != "" {
			revisions[fn.Physical] = fn.Revision
		}
	}
	if len(revisions) == 0 {
		return nil
	}
	return revisions
}

func physicalOf(containers []provider.AppContainer, app string) string {
	for _, container := range containers {
		if container.Name == app {
			return container.Physical
		}
	}
	return ""
}

func discoveredHealthPathOf(containers []provider.AppContainer, app string) string {
	for _, container := range containers {
		if container.Name == app {
			return container.DiscoveredHealthCheckPath
		}
	}
	return ""
}

func healthPathOf(entry provider.AppEntry, containers []provider.AppContainer) string {
	if entry.HealthCheckPath != "" {
		return entry.HealthCheckPath
	}
	return discoveredHealthPathOf(containers, entry.App)
}

func originOf(containers []provider.AppContainer, app string) string {
	for _, container := range containers {
		if container.Name == app {
			return container.URL
		}
	}
	return ""
}

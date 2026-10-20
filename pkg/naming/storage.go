package naming

import (
	"fmt"
	"strings"
)

type ObjectKind string

const (
	ObjectFunction    ObjectKind = "fn"
	ObjectBytecode    ObjectKind = "bytecode"
	ObjectPayloads    ObjectKind = "payloads"
	ObjectAssets      ObjectKind = "assets"
	ObjectPages       ObjectKind = "pages"
	ObjectRouteTables ObjectKind = "route-tables"
	ObjectBundles     ObjectKind = "bundles"
	ObjectImageConfig ObjectKind = "image-config"
	ObjectFetchCache  ObjectKind = "fetch-cache"
	ObjectUseCache    ObjectKind = "use-cache"
	ObjectTagClock    ObjectKind = "tag-clock"
)

type StorageClass string

const (
	StorageState     StorageClass = "state"
	StorageArtifacts StorageClass = "artifacts"
	StorageEdge      StorageClass = "edge"
	StorageCache     StorageClass = "cache"
)

var objectClasses = map[ObjectKind]StorageClass{
	ObjectFunction:    StorageArtifacts,
	ObjectBytecode:    StorageArtifacts,
	ObjectPayloads:    StorageArtifacts,
	ObjectAssets:      StorageEdge,
	ObjectPages:       StorageEdge,
	ObjectRouteTables: StorageEdge,
	ObjectBundles:     StorageEdge,
	ObjectImageConfig: StorageEdge,
	ObjectFetchCache:  StorageCache,
	ObjectUseCache:    StorageCache,
	ObjectTagClock:    StorageCache,
}

func (k ObjectKind) Class() StorageClass {
	return objectClasses[k]
}

var CoordinateObjectKinds = []ObjectKind{
	ObjectFunction,
	ObjectBytecode,
	ObjectAssets,
	ObjectPages,
	ObjectRouteTables,
	ObjectBundles,
	ObjectImageConfig,
	ObjectFetchCache,
	ObjectUseCache,
	ObjectTagClock,
}

var ISRObjectKinds = []ObjectKind{ObjectPages, ObjectFetchCache, ObjectUseCache, ObjectTagClock}

const (
	EdgeReadableTagKey   = "ocel:edge"
	EdgeReadableTagValue = "readable"
)

const ImageConfigFile = "image-config.json"

func (c Coordinate) StoragePrefix(kind ObjectKind) string {
	prefix := string(kind) + PathSeparator
	for _, field := range []string{c.Project, c.Env, c.App, c.Release.String()} {
		if field == "" {
			break
		}
		prefix += field + PathSeparator
	}
	return prefix
}

func (c Coordinate) StorageCoordinate() string {
	return strings.Join([]string{c.Project, c.Env, c.App, c.Release.String()}, PathSeparator)
}

func (c Coordinate) ObjectKey(kind ObjectKind, rest string) string {
	return c.StoragePrefix(kind) + strings.TrimPrefix(rest, PathSeparator)
}

func (c Coordinate) FunctionArtifactKey(sha string) string {
	return c.ObjectKey(ObjectFunction, Sanitize(c.Name)+PathSeparator+sha+".zip")
}

func (c Coordinate) AssetKey(assetPath string) string {
	return c.ObjectKey(ObjectAssets, assetPath)
}

func (c Coordinate) AssetPrefix() string {
	return strings.TrimSuffix(c.StoragePrefix(ObjectAssets), PathSeparator)
}

func (c Coordinate) PagesPrefix() string {
	return strings.TrimSuffix(c.StoragePrefix(ObjectPages), PathSeparator)
}

func (c Coordinate) ImageConfigKey() string {
	return c.ObjectKey(ObjectImageConfig, ImageConfigFile)
}

func (c Coordinate) RouteTableKey(digest string) string {
	return c.ObjectKey(ObjectRouteTables, digest+".json")
}

func (c Coordinate) BundleKey(file string) string {
	return c.ObjectKey(ObjectBundles, file)
}

func PayloadKey(payload, sha string) string {
	return string(ObjectPayloads) + PathSeparator + payload + PathSeparator + sha + ".zip"
}

type ObjectKey struct {
	Kind    ObjectKind
	Project string
	Env     string
	App     string
	Release ReleaseToken
	Rest    string
}

func (k ObjectKey) Coordinate() Coordinate {
	return Coordinate{Project: k.Project, Env: k.Env, App: k.App, Release: k.Release}
}

const coordinateSegments = 4

func ParseObjectKind(key string) (ObjectKind, error) {
	kind := ObjectKind(strings.SplitN(key, PathSeparator, 2)[0])
	if kind.Class() == "" {
		return "", fmt.Errorf("object key %q starts with %q, which is no kind of object Ocel stores", key, kind)
	}
	return kind, nil
}

func ParseObjectKey(key string) (ObjectKey, error) {
	segments := strings.SplitN(key, PathSeparator, coordinateSegments+2)
	if len(segments) < coordinateSegments+1 {
		return ObjectKey{}, fmt.Errorf("object key %q names no kind, project, env, app and release", key)
	}
	kind := ObjectKind(segments[0])
	if kind.Class() == "" || kind == ObjectPayloads {
		return ObjectKey{}, fmt.Errorf("object key %q starts with %q, which is no kind keyed by a coordinate", key, kind)
	}
	coordinate, err := ParseStorageCoordinate(strings.Join(segments[1:coordinateSegments+1], PathSeparator))
	if err != nil {
		return ObjectKey{}, fmt.Errorf("object key %q: %w", key, err)
	}
	parsed := ObjectKey{Kind: kind, Project: coordinate.Project, Env: coordinate.Env, App: coordinate.App, Release: coordinate.Release}
	if len(segments) == coordinateSegments+1 {
		return parsed, nil
	}
	parsed.Rest = segments[coordinateSegments+1]
	for _, segment := range strings.Split(parsed.Rest, PathSeparator) {
		if segment == "" || segment == "." || segment == ".." {
			return ObjectKey{}, fmt.Errorf("object key %q has an empty, %q or %q segment after its release", key, ".", "..")
		}
	}
	return parsed, nil
}

func ParseStorageCoordinate(value string) (Coordinate, error) {
	fields := strings.Split(value, PathSeparator)
	if len(fields) != coordinateSegments {
		return Coordinate{}, fmt.Errorf("storage coordinate %q is not <project>/<env>/<app>/<release token>", value)
	}
	for i, name := range []string{"project", "env", "app"} {
		if err := Validate(name, fields[i]); err != nil {
			return Coordinate{}, err
		}
	}
	release, err := ParseReleaseToken(fields[3])
	if err != nil {
		return Coordinate{}, err
	}
	return Coordinate{Project: fields[0], Env: fields[1], App: fields[2], Release: release}, nil
}

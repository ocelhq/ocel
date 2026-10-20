package naming

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
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

const CacheObjectLifetimeDays = 30

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

var ExpiringObjectKinds = []ObjectKind{ObjectFetchCache, ObjectUseCache}

const ImageConfigFile = "image-config.json"

type Buckets map[StorageClass]string

func (b Buckets) Holding(kind ObjectKind) string {
	return b[kind.Class()]
}

const StorageCoordinateVar = "OCEL_STORAGE_COORDINATE"

var runtimeBucketVars = map[StorageClass]string{
	StorageArtifacts: "OCEL_ARTIFACTS_BUCKET",
	StorageEdge:      "OCEL_EDGE_BUCKET",
	StorageCache:     "OCEL_CACHE_BUCKET",
}

func (c StorageClass) BucketVar() string {
	return runtimeBucketVars[c]
}

func StorageEnv(storage Coordinate, buckets Buckets) map[string]string {
	env := map[string]string{StorageCoordinateVar: storage.StorageCoordinate()}
	for class, variable := range runtimeBucketVars {
		if bucket := buckets[class]; bucket != "" {
			env[variable] = bucket
		}
	}
	return env
}

func (c Coordinate) StoragePrefix(kind ObjectKind) string {
	return string(kind) + PathSeparator + c.StorageCoordinate() + PathSeparator
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

type StorageScope struct {
	project string
	env     string
	app     string
	release ReleaseToken
}

func ProjectScope(project string) (StorageScope, error) {
	if err := Validate("project", project); err != nil {
		return StorageScope{}, err
	}
	return StorageScope{project: project}, nil
}

func EnvScope(project, env string) (StorageScope, error) {
	scope, err := ProjectScope(project)
	if err != nil {
		return StorageScope{}, err
	}
	if err := Validate("env", env); err != nil {
		return StorageScope{}, err
	}
	scope.env = env
	return scope, nil
}

func AppScope(c Coordinate) (StorageScope, error) {
	scope, err := EnvScope(c.Project, c.Env)
	if err != nil {
		return StorageScope{}, err
	}
	if err := Validate("app", c.App); err != nil {
		return StorageScope{}, err
	}
	scope.app = c.App
	return scope, nil
}

func ReleaseScope(c Coordinate) (StorageScope, error) {
	scope, err := AppScope(c)
	if err != nil {
		return StorageScope{}, err
	}
	if c.Release.IsZero() {
		return StorageScope{}, fmt.Errorf("the objects of %s/%s/%s name no release", c.Project, c.Env, c.App)
	}
	scope.release = c.Release
	return scope, nil
}

func (s StorageScope) Project() string { return s.project }

func (s StorageScope) Prefix(kind ObjectKind) (string, error) {
	if s.project == "" {
		return "", refusal.Refuse(refusal.CodeInvalid, "a storage scope naming no project names every project's objects")
	}
	prefix := string(kind) + PathSeparator
	for _, field := range []string{s.project, s.env, s.app, s.release.String()} {
		if field != "" {
			prefix += field + PathSeparator
		}
	}
	return prefix, nil
}

type ParsedObjectKey struct {
	Kind       ObjectKind
	Coordinate Coordinate
	Rest       string
}

const coordinateSegments = 4

func ParseObjectKind(key string) (ObjectKind, error) {
	kind := ObjectKind(strings.SplitN(key, PathSeparator, 2)[0])
	if kind.Class() == "" {
		return "", fmt.Errorf("object key %q starts with %q, which is no kind of object Ocel stores", key, kind)
	}
	return kind, nil
}

func ParseObjectKey(key string) (ParsedObjectKey, error) {
	segments := strings.SplitN(key, PathSeparator, coordinateSegments+2)
	if len(segments) < coordinateSegments+2 {
		return ParsedObjectKey{}, fmt.Errorf("object key %q names no kind, project, env, app, release and object", key)
	}
	kind := ObjectKind(segments[0])
	if kind.Class() == "" || kind == ObjectPayloads {
		return ParsedObjectKey{}, fmt.Errorf("object key %q starts with %q, which is no kind keyed by a coordinate", key, kind)
	}
	coordinate, err := ParseStorageCoordinate(strings.Join(segments[1:coordinateSegments+1], PathSeparator))
	if err != nil {
		return ParsedObjectKey{}, fmt.Errorf("object key %q: %w", key, err)
	}
	rest := segments[coordinateSegments+1]
	for _, segment := range strings.Split(rest, PathSeparator) {
		if segment == "" || segment == "." || segment == ".." {
			return ParsedObjectKey{}, fmt.Errorf("object key %q has an empty, %q or %q segment after its release", key, ".", "..")
		}
	}
	return ParsedObjectKey{Kind: kind, Coordinate: coordinate, Rest: rest}, nil
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

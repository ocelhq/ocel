package deploy

import (
	"context"
	"fmt"
	"math"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type transformedArgs struct {
	functions map[string]functionArgs
	buckets   map[string]bucketArgs
	postgres  map[string]postgresArgs
}

type transformCandidate struct {
	name  string
	apply func(*transformedArgs, transform.Result) error
}

func resolveTransforms(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) (*transformedArgs, error) {
	if cfg.Transform == nil {
		return nil, nil
	}

	req := transform.Request{EnvClass: envClass(cfg.Class), Env: cfg.Env}
	var candidates []transformCandidate

	for _, r := range manifest.GetResources() {
		name := r.GetLogicalName()
		switch {
		case r.GetPostgres() != nil:
			args := translatePostgres(r.GetPostgres())
			req.Resources = append(req.Resources, transform.Resource{
				Type: "postgres", Name: name, Surfaces: postgresSurfaces(args),
			})
			candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
				applied, err := applyPostgresSurfaces(args, result)
				out.postgres[name] = applied
				return err
			}})
		case r.GetBucket() != nil:
			args := translateBucket(r.GetBucket())
			req.Resources = append(req.Resources, transform.Resource{
				Type: "bucket", Name: name, Surfaces: bucketSurfaces(args),
			})
			candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
				applied, err := applyBucketSurfaces(args, result)
				out.buckets[name] = applied
				return err
			}})
		}
	}

	for _, fn := range manifest.GetFunctions() {
		name := fn.GetLogicalName()
		args := translateFunction(fn)
		req.Resources = append(req.Resources, transform.Resource{
			Type: "function", Name: name, App: fn.GetApp(), Surfaces: functionSurfaces(args),
		})
		candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
			applied, err := applyFunctionSurfaces(args, result)
			out.functions[name] = applied
			return err
		}})
	}

	results, err := cfg.Transform.Evaluate(ctx, req)
	if err != nil {
		return nil, err
	}
	if results == nil {
		return nil, nil
	}
	if len(results) != len(candidates) {
		return nil, fmt.Errorf("transforms returned %d results for %d resources", len(results), len(candidates))
	}

	out := &transformedArgs{
		functions: map[string]functionArgs{},
		buckets:   map[string]bucketArgs{},
		postgres:  map[string]postgresArgs{},
	}
	for i, c := range candidates {
		if err := c.apply(out, results[i]); err != nil {
			return nil, fmt.Errorf("transform %s: %w", c.name, err)
		}
	}
	return out, nil
}

func (t *transformedArgs) forFunction(fn *deploymentsv1.ManifestFunction) functionArgs {
	if t != nil {
		if args, ok := t.functions[fn.GetLogicalName()]; ok {
			return args
		}
	}
	return translateFunction(fn)
}

func (t *transformedArgs) forBucket(logicalName string, cfg *resourcesv1.BucketConfig) bucketArgs {
	if t != nil {
		if args, ok := t.buckets[logicalName]; ok {
			return args
		}
	}
	return translateBucket(cfg)
}

func (t *transformedArgs) forPostgres(logicalName string, cfg *resourcesv1.PostgresConfig) postgresArgs {
	if t != nil {
		if args, ok := t.postgres[logicalName]; ok {
			return args
		}
	}
	return translatePostgres(cfg)
}

func functionSurfaces(a functionArgs) transform.Surfaces {
	return transform.Surfaces{
		"lambda": {
			"memorySizeMb":   a.MemorySizeMB,
			"timeoutSeconds": a.TimeoutSeconds,
			"runtime":        a.Runtime,
		},
		"url": {"invokeMode": a.InvokeMode},
	}
}

func applyFunctionSurfaces(a functionArgs, result transform.Result) (functionArgs, error) {
	a.Tags = result.Tags
	s := result.Surfaces
	lambda, err := surfaceAt(s, "lambda")
	if err != nil {
		return a, err
	}
	if a.MemorySizeMB, err = surfaceInt(lambda, "lambda", "memorySizeMb"); err != nil {
		return a, err
	}
	if a.TimeoutSeconds, err = surfaceInt(lambda, "lambda", "timeoutSeconds"); err != nil {
		return a, err
	}
	if a.Runtime, err = surfaceString(lambda, "lambda", "runtime"); err != nil {
		return a, err
	}

	url, err := surfaceAt(s, "url")
	if err != nil {
		return a, err
	}
	if a.InvokeMode, err = surfaceString(url, "url", "invokeMode"); err != nil {
		return a, err
	}
	return a, nil
}

func bucketSurfaces(a bucketArgs) transform.Surfaces {
	return transform.Surfaces{
		"bucket": {"forceDestroy": a.ForceDestroy},
		"cors": {
			"allowedOrigins": surfaceList(a.CORS.AllowedOrigins),
			"allowedMethods": surfaceList(a.CORS.AllowedMethods),
			"allowedHeaders": surfaceList(a.CORS.AllowedHeaders),
			"exposeHeaders":  surfaceList(a.CORS.ExposeHeaders),
			"maxAgeSeconds":  a.CORS.MaxAgeSeconds,
		},
		"listener":     {"timeoutSeconds": a.ListenerTimeoutSeconds},
		"notification": {"events": surfaceList(a.NotificationEvents)},
	}
}

func applyBucketSurfaces(a bucketArgs, result transform.Result) (bucketArgs, error) {
	a.Tags = result.Tags
	s := result.Surfaces
	bucket, err := surfaceAt(s, "bucket")
	if err != nil {
		return a, err
	}
	if a.ForceDestroy, err = surfaceBool(bucket, "bucket", "forceDestroy"); err != nil {
		return a, err
	}

	cors, err := surfaceAt(s, "cors")
	if err != nil {
		return a, err
	}
	if a.CORS.AllowedOrigins, err = surfaceStrings(cors, "cors", "allowedOrigins"); err != nil {
		return a, err
	}
	if a.CORS.AllowedMethods, err = surfaceStrings(cors, "cors", "allowedMethods"); err != nil {
		return a, err
	}
	if a.CORS.AllowedHeaders, err = surfaceStrings(cors, "cors", "allowedHeaders"); err != nil {
		return a, err
	}
	if a.CORS.ExposeHeaders, err = surfaceStrings(cors, "cors", "exposeHeaders"); err != nil {
		return a, err
	}
	if a.CORS.MaxAgeSeconds, err = surfaceInt(cors, "cors", "maxAgeSeconds"); err != nil {
		return a, err
	}
	a.AllowedOrigins = a.CORS.AllowedOrigins

	listener, err := surfaceAt(s, "listener")
	if err != nil {
		return a, err
	}
	if a.ListenerTimeoutSeconds, err = surfaceInt(listener, "listener", "timeoutSeconds"); err != nil {
		return a, err
	}

	notification, err := surfaceAt(s, "notification")
	if err != nil {
		return a, err
	}
	if a.NotificationEvents, err = surfaceStrings(notification, "notification", "events"); err != nil {
		return a, err
	}
	return a, nil
}

func postgresSurfaces(a postgresArgs) transform.Surfaces {
	return transform.Surfaces{
		"cluster": {
			"engineVersion":      a.EngineVersion,
			"minCapacity":        a.MinCapacity,
			"maxCapacity":        a.MaxCapacity,
			"deletionProtection": a.DeletionProtection,
			"skipFinalSnapshot":  a.SkipFinalSnapshot,
		},
		"instance": {
			"instanceClass":      a.InstanceClass,
			"publiclyAccessible": a.PubliclyAccessible,
		},
	}
}

func applyPostgresSurfaces(a postgresArgs, result transform.Result) (postgresArgs, error) {
	a.Tags = result.Tags
	s := result.Surfaces
	cluster, err := surfaceAt(s, "cluster")
	if err != nil {
		return a, err
	}
	if a.EngineVersion, err = surfaceString(cluster, "cluster", "engineVersion"); err != nil {
		return a, err
	}
	if a.MinCapacity, err = surfaceFloat(cluster, "cluster", "minCapacity"); err != nil {
		return a, err
	}
	if a.MaxCapacity, err = surfaceFloat(cluster, "cluster", "maxCapacity"); err != nil {
		return a, err
	}
	if a.DeletionProtection, err = surfaceBool(cluster, "cluster", "deletionProtection"); err != nil {
		return a, err
	}
	if a.SkipFinalSnapshot, err = surfaceBool(cluster, "cluster", "skipFinalSnapshot"); err != nil {
		return a, err
	}

	instance, err := surfaceAt(s, "instance")
	if err != nil {
		return a, err
	}
	if a.InstanceClass, err = surfaceString(instance, "instance", "instanceClass"); err != nil {
		return a, err
	}
	if a.PubliclyAccessible, err = surfaceBool(instance, "instance", "publiclyAccessible"); err != nil {
		return a, err
	}
	return a, nil
}

func surfaceAt(s transform.Surfaces, key string) (map[string]any, error) {
	args, ok := s[key]
	if !ok {
		return nil, fmt.Errorf("transforms dropped the %s args", key)
	}
	return args, nil
}

func surfaceValue(m map[string]any, key, field string) (any, error) {
	value, ok := m[field]
	if !ok {
		return nil, fmt.Errorf("transforms dropped %s.%s", key, field)
	}
	return value, nil
}

func surfaceFloat(m map[string]any, key, field string) (float64, error) {
	value, err := surfaceValue(m, key, field)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%s.%s must be a number, got %T", key, field, value)
	}
	return number, nil
}

func surfaceInt(m map[string]any, key, field string) (int, error) {
	number, err := surfaceFloat(m, key, field)
	if err != nil {
		return 0, err
	}
	if number != math.Trunc(number) {
		return 0, fmt.Errorf("%s.%s must be a whole number, got %v", key, field, number)
	}
	return int(number), nil
}

func surfaceString(m map[string]any, key, field string) (string, error) {
	value, err := surfaceValue(m, key, field)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string, got %T", key, field, value)
	}
	return text, nil
}

func surfaceBool(m map[string]any, key, field string) (bool, error) {
	value, err := surfaceValue(m, key, field)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s.%s must be true or false, got %T", key, field, value)
	}
	return flag, nil
}

func surfaceList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func surfaceStrings(m map[string]any, key, field string) ([]string, error) {
	value, err := surfaceValue(m, key, field)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s.%s must be a list of strings, got %T", key, field, value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a list of strings, got a %T in it", key, field, item)
		}
		out = append(out, text)
	}
	return out, nil
}

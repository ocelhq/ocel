package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	abandonedUploadDays  = 1
	expiredSessionDays   = 1
	uploadCORSMaxAge     = time.Hour
	objectsDeletedAtOnce = 16
)

var (
	uploadMethods = []string{http.MethodPut, http.MethodPost}
	uploadHeaders = []string{"Content-Type", "Content-Disposition", "Cache-Control", "ETag", "x-goog-content-length-range", "x-goog-if-generation-match"}
)

type appBucket struct {
	name  string
	attrs storage.BucketAttrs
}

func (p *Provider) declaredBucket(clients *clients, in resources.ProvisionRequest) appBucket {
	spec := provider.BucketSpec{}
	if in.Resource.Bucket != nil {
		spec = *in.Resource.Bucket
	}
	return appBucket{
		name: clients.AppBucket(in.Ref.Project, in.Ref.Name.Env, in.Resource.Name),
		attrs: storage.BucketAttrs{
			Location:                 clients.region,
			Labels:                   labelsFor(clients.Names, in.Ref, bucketLabel, in.Resource.Name),
			UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
			PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
			CORS:                     uploadCORS(spec.AllowedOrigins),
			Lifecycle:                bucketLifecycle(),
		},
	}
}

func uploadCORS(origins []string) []storage.CORS {
	if len(origins) == 0 {
		return nil
	}
	return []storage.CORS{{
		Origins:         slices.Clone(origins),
		Methods:         slices.Clone(uploadMethods),
		ResponseHeaders: slices.Clone(uploadHeaders),
		MaxAge:          uploadCORSMaxAge,
	}}
}

func bucketLifecycle() storage.Lifecycle {
	return storage.Lifecycle{Rules: []storage.LifecycleRule{
		{
			Action:    storage.LifecycleAction{Type: storage.AbortIncompleteMPUAction},
			Condition: storage.LifecycleCondition{AgeInDays: abandonedUploadDays},
		},
		{
			Action:    storage.LifecycleAction{Type: storage.DeleteAction},
			Condition: storage.LifecycleCondition{AgeInDays: expiredSessionDays, MatchesPrefix: []string{s3store.SessionKeyPrefix}},
		},
	}}
}

func (b appBucket) drift(current *storage.BucketAttrs) (storage.BucketAttrsToUpdate, []string) {
	var update storage.BucketAttrsToUpdate
	var differences []string
	if !current.UniformBucketLevelAccess.Enabled {
		update.UniformBucketLevelAccess = &b.attrs.UniformBucketLevelAccess
		differences = append(differences, "uniform bucket-level access turns on")
	}
	if current.PublicAccessPrevention != b.attrs.PublicAccessPrevention {
		update.PublicAccessPrevention = b.attrs.PublicAccessPrevention
		differences = append(differences, fmt.Sprintf("public access prevention changes from %s to %s", current.PublicAccessPrevention, b.attrs.PublicAccessPrevention))
	}
	if !sameCORS(current.CORS, b.attrs.CORS) {
		update.CORS = b.attrs.CORS
		if update.CORS == nil {
			update.CORS = []storage.CORS{}
		}
		differences = append(differences, describeCORSChange(current.CORS, b.attrs.CORS))
	}
	if !sameLifecycle(current.Lifecycle, b.attrs.Lifecycle) {
		lifecycle := b.attrs.Lifecycle
		update.Lifecycle = &lifecycle
		differences = append(differences, fmt.Sprintf("lifecycle rules change from %s to %s", describeRules(current.Lifecycle), describeRules(b.attrs.Lifecycle)))
	}
	return update, differences
}

func listOrigins(cors []storage.CORS) []string {
	origins := []string{}
	for _, rule := range cors {
		origins = append(origins, rule.Origins...)
	}
	return origins
}

func describeCORSChange(current, desired []storage.CORS) string {
	from, to := listOrigins(current), listOrigins(desired)
	if !slices.Equal(from, to) {
		return fmt.Sprintf("allowed origins change from %v to %v", from, to)
	}
	return fmt.Sprintf("the CORS methods, response headers or max age for origins %v change", to)
}

func describeRules(lifecycle storage.Lifecycle) string {
	rules := make([]string, 0, len(lifecycle.Rules))
	for _, rule := range lifecycle.Rules {
		described := fmt.Sprintf("%s after %d day(s)", rule.Action.Type, rule.Condition.AgeInDays)
		if len(rule.Condition.MatchesPrefix) > 0 {
			described += fmt.Sprintf(" under %v", rule.Condition.MatchesPrefix)
		}
		rules = append(rules, described)
	}
	return fmt.Sprintf("%v", rules)
}

func sameCORS(current, desired []storage.CORS) bool {
	return slices.EqualFunc(current, desired, func(a, b storage.CORS) bool {
		return slices.Equal(a.Origins, b.Origins) && slices.Equal(a.Methods, b.Methods) &&
			slices.Equal(a.ResponseHeaders, b.ResponseHeaders) && a.MaxAge == b.MaxAge
	})
}

func sameLifecycle(current, desired storage.Lifecycle) bool {
	return slices.EqualFunc(current.Rules, desired.Rules, func(a, b storage.LifecycleRule) bool {
		return a.Action.Type == b.Action.Type && a.Condition.AgeInDays == b.Condition.AgeInDays &&
			slices.Equal(a.Condition.MatchesPrefix, b.Condition.MatchesPrefix)
	})
}

func (p *Provider) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	client, err := clients.Storage()
	if err != nil {
		return provider.Binding{}, err
	}
	declared := p.declaredBucket(clients, in)
	if err := declared.ensure(ctx, client, clients.project, in.Resource.Name, progress); err != nil {
		return provider.Binding{}, err
	}
	return declared.binding(in.Resource), nil
}

func (p *Provider) PlanBucket(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return "", "", err
	}
	client, err := clients.Storage()
	if err != nil {
		return "", "", err
	}
	return p.declaredBucket(clients, in).plan(ctx, client, in.Resource.Name)
}

func (b appBucket) plan(ctx context.Context, client *storage.Client, declared string) (provider.ChangeAction, string, error) {
	current, err := b.read(ctx, client, declared)
	if err != nil {
		return "", "", err
	}
	if current == nil {
		return provider.ActionCreate, "", nil
	}
	if _, differences := b.drift(current); len(differences) > 0 {
		return provider.ActionUpdate, strings.Join(differences, "; "), nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func (b appBucket) read(ctx context.Context, client *storage.Client, declared string) (*storage.BucketAttrs, error) {
	current, err := client.Bucket(b.name).Attrs(ctx)
	if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Storage bucket %s that bucket %s stores into: %w", b.name, declared, err)
	}
	return current, nil
}

func (b appBucket) ensure(ctx context.Context, client *storage.Client, project, declared string, progress progress.Log) error {
	current, err := b.read(ctx, client, declared)
	if err != nil {
		return err
	}
	if current == nil {
		return b.create(ctx, client, project, declared, progress)
	}
	update, differences := b.drift(current)
	if len(differences) == 0 {
		return nil
	}
	ensureProgress(progress).Say("Updating bucket " + declared + " on Cloud Storage bucket " + b.name + ": " + strings.Join(differences, "; "))
	if _, err := client.Bucket(b.name).Update(ctx, update); err != nil {
		return fmt.Errorf("update the Cloud Storage bucket %s that bucket %s stores into: %w", b.name, declared, err)
	}
	return nil
}

func (b appBucket) create(ctx context.Context, client *storage.Client, project, declared string, progress progress.Log) error {
	ensureProgress(progress).Say("Creating bucket " + declared + " as Cloud Storage bucket " + b.name + " in " + b.attrs.Location)
	attrs := b.attrs
	err := client.Bucket(b.name).Create(ctx, project, &attrs)
	if err == nil {
		return nil
	}
	if !taken(err) {
		return fmt.Errorf("create the Cloud Storage bucket %s that bucket %s stores into: %w", b.name, declared, err)
	}
	if _, read := client.Bucket(b.name).Attrs(ctx); read == nil {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"bucket %s would be the Cloud Storage bucket %s, and another Google Cloud project already holds that name: bucket names are global.\n"+
			"Rename the bucket, or the project, and deploy again", declared, b.name)
}

func (b appBucket) binding(resource provider.Resource) provider.Binding {
	return provider.Binding{
		Type:       provider.BindingBucket,
		Name:       resource.Name,
		Resource:   resource.Declared,
		Properties: map[string]string{provider.PropertyBucket: b.name},
	}
}

func (p *Provider) RemoveBucket(ctx context.Context, _ provider.StackRef, binding provider.Binding, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	client, err := clients.Storage()
	if err != nil {
		return err
	}
	name := binding.Properties[provider.PropertyBucket]
	ensureProgress(progress).Say("Removing bucket " + binding.Name + ", its Cloud Storage bucket " + name + " and every object in it")
	handle := client.Bucket(name)
	if err := emptyBucket(ctx, handle); err != nil {
		return fmt.Errorf("empty the Cloud Storage bucket %s that bucket %s stores into: %w", name, binding.Name, err)
	}
	err = handle.Delete(ctx)
	if err == nil || errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return nil
	}
	return fmt.Errorf("delete the Cloud Storage bucket %s that bucket %s stores into: %w", name, binding.Name, err)
}

func emptyBucket(ctx context.Context, handle *storage.BucketHandle) error {
	deleting, ctx := errgroup.WithContext(ctx)
	deleting.SetLimit(objectsDeletedAtOnce)
	objects := handle.Objects(ctx, &storage.Query{Projection: storage.ProjectionNoACL})
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
			return deleting.Wait()
		}
		if err != nil {
			return errors.Join(err, deleting.Wait())
		}
		deleting.Go(func() error {
			err := handle.Object(attrs.Name).Delete(ctx)
			if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
				return fmt.Errorf("delete %s: %w", attrs.Name, err)
			}
			return nil
		})
	}
	return deleting.Wait()
}

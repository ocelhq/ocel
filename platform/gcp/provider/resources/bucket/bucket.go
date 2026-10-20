package bucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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

const RootBuckets records.Root = "buckets"

type appBucket struct {
	key      records.Key
	mint     func(tag string) (string, error)
	attrs    storage.BucketAttrs
	emulated bool
}

type recordedBucket struct {
	Bucket string `json:"bucket"`
}

func bucketKey(ref provider.StackRef, bucket string) records.Key {
	return records.Partition{Tier: ref.Tier, Root: RootBuckets}.Key(ref.Project, ref.Name.Env, bucket)
}

func (p *Bucket) declaredBucket(clients *gcloud.Clients, in resources.ProvisionRequest) (appBucket, error) {
	spec := provider.BucketSpec{}
	if in.Resource.Bucket != nil {
		spec = *in.Resource.Bucket
	}
	labels, err := names.LabelsFor(clients.Names, in.Ref, names.BucketLabel, in.Resource.Name)
	if err != nil {
		return appBucket{}, err
	}
	return appBucket{
		key: bucketKey(in.Ref, in.Resource.Name),
		mint: func(tag string) (string, error) {
			return clients.AppBucket(in.Ref.Project, in.Ref.Name.Env, in.Resource.Name, tag)
		},
		emulated: clients.Emulated(),
		attrs: storage.BucketAttrs{
			Location:                 clients.Region,
			Labels:                   labels,
			UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
			PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
			CORS:                     uploadCORS(spec.AllowedOrigins),
			Lifecycle:                bucketLifecycle(),
		},
	}, nil
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
	if !b.keepsNoAccess(current) && !current.UniformBucketLevelAccess.Enabled {
		update.UniformBucketLevelAccess = &b.attrs.UniformBucketLevelAccess
		differences = append(differences, "uniform bucket-level access turns on")
	}
	if !b.keepsNoAccess(current) && current.PublicAccessPrevention != b.attrs.PublicAccessPrevention {
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

func (b appBucket) keepsNoAccess(current *storage.BucketAttrs) bool {
	return b.emulated && current.PublicAccessPrevention == storage.PublicAccessPreventionUnknown
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

func (p *Bucket) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	client, err := clients.Storage()
	if err != nil {
		return provider.Binding{}, err
	}
	declared, err := p.declaredBucket(clients, in)
	if err != nil {
		return provider.Binding{}, err
	}
	name, err := declared.ensure(ctx, p.records, client, clients.Project(), in.Resource.Name, progress)
	if err != nil {
		return provider.Binding{}, err
	}
	return bucketBinding(in.Resource, name), nil
}

func (p *Bucket) Plan(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return "", "", err
	}
	client, err := clients.Storage()
	if err != nil {
		return "", "", err
	}
	declared, err := p.declaredBucket(clients, in)
	if err != nil {
		return "", "", err
	}
	return declared.plan(ctx, p.records, client, in.Resource.Name)
}

func (b appBucket) plan(ctx context.Context, store records.Store, client *storage.Client, declared string) (provider.ChangeAction, string, error) {
	name, err := b.recorded(ctx, store, declared)
	if err != nil || name == "" {
		return provider.ActionCreate, "", err
	}
	current, err := b.read(ctx, client, name, declared)
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

func (b appBucket) recorded(ctx context.Context, store records.Store, declared string) (string, error) {
	entry, err := records.ReadOrEmpty(ctx, store, b.key)
	if err != nil {
		return "", fmt.Errorf("read the Cloud Storage bucket recorded for bucket %s: %w", declared, err)
	}
	return readRecordedBucket(entry, declared)
}

func readRecordedBucket(entry records.Entry, declared string) (string, error) {
	if len(entry.Value) == 0 {
		return "", nil
	}
	var recorded recordedBucket
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return "", fmt.Errorf("read the Cloud Storage bucket recorded for bucket %s: %w", declared, err)
	}
	return recorded.Bucket, nil
}

func (b appBucket) claim(ctx context.Context, store records.Store, declared string) (string, bool, error) {
	var claimed string
	var minted bool
	err := records.Change(ctx, store, b.key, func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) > 0 {
			recorded, err := readRecordedBucket(entry, declared)
			claimed, minted = recorded, false
			return nil, false, err
		}
		name, err := b.mint(naming.NewRandomTag())
		if err != nil {
			return nil, false, err
		}
		claimed, minted = name, true
		value, err := json.Marshal(recordedBucket{Bucket: name})
		return value, err == nil, err
	})
	if err != nil {
		return "", false, err
	}
	return claimed, minted, nil
}

func (b appBucket) read(ctx context.Context, client *storage.Client, name, declared string) (*storage.BucketAttrs, error) {
	current, err := client.Bucket(name).Attrs(ctx)
	if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Storage bucket %s that bucket %s stores into: %w", name, declared, err)
	}
	return current, nil
}

func (b appBucket) ensure(ctx context.Context, store records.Store, client *storage.Client, project, declared string, progress progress.Log) (string, error) {
	name, minted, err := b.claim(ctx, store, declared)
	if err != nil {
		return "", err
	}
	current, err := b.read(ctx, client, name, declared)
	if gcloud.Forbidden(err) && !minted {
		return "", refuseUnreadable(project, name, declared)
	}
	if gcloud.Forbidden(err) {
		return "", refuseHeldElsewhere(ctx, store, b.key, name, declared)
	}
	if err != nil {
		return "", err
	}
	if current == nil {
		return name, b.create(ctx, store, client, project, name, declared, progress)
	}
	update, differences := b.drift(current)
	if len(differences) == 0 {
		return name, nil
	}
	gcloud.EnsureProgress(progress).Say("Updating bucket " + declared + " on Cloud Storage bucket " + name + ": " + strings.Join(differences, "; "))
	if _, err := client.Bucket(name).Update(ctx, update); err != nil {
		return "", fmt.Errorf("update the Cloud Storage bucket %s that bucket %s stores into: %w", name, declared, err)
	}
	return name, nil
}

func (b appBucket) create(ctx context.Context, store records.Store, client *storage.Client, project, name, declared string, progress progress.Log) error {
	gcloud.EnsureProgress(progress).Say("Creating bucket " + declared + " as Cloud Storage bucket " + name + " in " + b.attrs.Location)
	attrs := b.attrs
	err := client.Bucket(name).Create(ctx, project, &attrs)
	if err == nil {
		return nil
	}
	if !gcloud.Taken(err) {
		return fmt.Errorf("create the Cloud Storage bucket %s that bucket %s stores into: %w", name, declared, err)
	}
	_, err = client.Bucket(name).Attrs(ctx)
	if err == nil {
		return nil
	}
	if !gcloud.Forbidden(err) {
		return fmt.Errorf("read the Cloud Storage bucket %s that bucket %s stores into, which Cloud Storage answered already exists: %w", name, declared, err)
	}
	return refuseHeldElsewhere(ctx, store, b.key, name, declared)
}

func refuseUnreadable(project, name, declared string) error {
	return refusal.Refuse(refusal.CodeDenied,
		"bucket %s stores into the Cloud Storage bucket %s, and Google Cloud answers that it exists but these credentials cannot read it.\n"+
			"Check that they hold storage.buckets.get on %s in project %s, then deploy again: the name stays recorded, since forgetting it would leave the bucket's objects behind",
		declared, name, name, project)
}

func refuseHeldElsewhere(ctx context.Context, store records.Store, key records.Key, name, declared string) error {
	if err := forgetBucket(ctx, store, key, name); err != nil {
		return err
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"bucket %s would be the Cloud Storage bucket %s, and another Google Cloud project already holds that name: bucket names are global.\n"+
			"Deploy again, which names the bucket with a new random tag", declared, name)
}

func forgetBucket(ctx context.Context, store records.Store, key records.Key, name string) error {
	return records.ForgetMatching(ctx, store, key, func(entry records.Entry) (bool, error) {
		var recorded recordedBucket
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return false, fmt.Errorf("read %s: %w", key, err)
		}
		return recorded.Bucket == name, nil
	})
}

func bucketBinding(resource provider.Resource, name string) provider.Binding {
	return provider.Binding{
		Type:       provider.BindingBucket,
		Name:       resource.Name,
		Resource:   resource.Declared,
		Properties: map[string]string{provider.PropertyBucket: name},
	}
}

func (p *Bucket) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	client, err := clients.Storage()
	if err != nil {
		return err
	}
	name := binding.Properties[provider.PropertyBucket]
	gcloud.EnsureProgress(progress).Say("Removing bucket " + binding.Name + ", its Cloud Storage bucket " + name + " and every object in it")
	handle := client.Bucket(name)
	if err := emptyBucket(ctx, handle); err != nil {
		return fmt.Errorf("empty the Cloud Storage bucket %s that bucket %s stores into: %w", name, binding.Name, err)
	}
	if err := handle.Delete(ctx); err != nil && !errors.Is(err, storage.ErrBucketNotExist) && !gcloud.Absent(err) {
		return fmt.Errorf("delete the Cloud Storage bucket %s that bucket %s stores into: %w", name, binding.Name, err)
	}
	return forgetBucket(ctx, p.records, bucketKey(ref, binding.Name), name)
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
		if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
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

type Bucket struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
	records     records.Store
}

func New(openClients func(ctx context.Context) (*gcloud.Clients, error), store records.Store) *Bucket {
	return &Bucket{openClients: openClients, records: store}
}

func (p *Bucket) Component() component.Component {
	return component.Component{
		Types:     []provider.BindingType{provider.BindingBucket},
		Hooks:     resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		Preflight: refusePublicBucket,
		Shape:     shapeBuckets,
		Grants: func(_ context.Context, app component.App) ([]grants.Grant, error) {
			return bucketGrants(app.Spec, app.Account, app.Member), nil
		},
	}
}

func refusePublicBucket(resource provider.Resource) error {
	if resource.Bucket != nil && resource.Bucket.Public {
		return refusal.Refuse(refusal.CodeInvalid,
			"bucket %s asks to be public, and this provider creates its buckets under public access prevention, which refuses any grant to allUsers: "+
				"serve the objects through your app or a signed url instead, or drop `public` from %s",
			resource.Name, resource.Name)
	}
	return nil
}

func shapeBuckets(shape component.Shape, declared []provider.Resource) error {
	for _, resource := range declared {
		bucket, err := shape.Names.UntaggedAppBucket(shape.Request.Deploy.Slug, shape.Request.Deploy.Env, resource.Name)
		if err != nil {
			return err
		}
		shape.Tree.Add(shape.Environment, shape.Vendor, cost.StorageBucketType, bucket, shape.Region,
			map[string]any{"location": shape.Region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": false}})
	}
	return nil
}

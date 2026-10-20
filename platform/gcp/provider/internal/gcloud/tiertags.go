package gcloud

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	TierTagUserRole     = "roles/resourcemanager.tagUser"
	globalTagEndpoint   = "https://cloudresourcemanager.googleapis.com/"
	tierTagSeenAttempts = 10
)

type TaggedResource struct {
	Name             string
	Regional         bool
	PermissionPrefix string
}

func (c *Clients) TierTagged() bool { return !c.Emulated() || c.TierTagEndpoint != "" }

func (c *Clients) InlineTierTags(tier environment.Tier) map[string]string {
	if !c.TierTagged() {
		return nil
	}
	return map[string]string{c.TierTagKeyPath(): string(tier)}
}

func (c *Clients) TagEndpointOf(target TaggedResource) string {
	if !target.Regional {
		return globalTagEndpoint
	}
	return "https://" + c.Region + "-cloudresourcemanager.googleapis.com/"
}

type tierTagsRead struct {
	mu     sync.Mutex
	values map[string]string
}

func (c *Clients) GlobalTagService() (*crm.Service, error) {
	return c.openTagService(&c.globalTags, globalTagEndpoint)
}

func (c *Clients) tagServiceOf(target TaggedResource) (*crm.Service, error) {
	if !target.Regional {
		return c.GlobalTagService()
	}
	return c.openTagService(&c.regionalTags, c.TagEndpointOf(target))
}

func (c *Clients) openTagService(cache *Memo[*crm.Service], endpoint string) (*crm.Service, error) {
	options := ports.EmulatorREST(c.TierTagEndpoint)
	if !c.Emulated() {
		options = []option.ClientOption{option.WithEndpoint(endpoint)}
	}
	return opened(c, cache, "Resource Manager tags", func() (*crm.Service, error) {
		return crm.NewService(context.Background(), options...)
	})
}

type tagValue struct {
	short   string
	marks   string
	command string
}

func tierValue(tier environment.Tier) tagValue {
	return tagValue{short: string(tier), marks: "every resource of the " + string(tier) + " tier", command: provider.BootstrapCommand(tier)}
}

var outsideValue = tagValue{short: names.OutsideValue, marks: "every service account of the project ocel does not make", command: "ocel bootstrap"}

func (c *Clients) valuePath(value tagValue) string { return c.TierTagKeyPath() + "/" + value.short }

func (c *Clients) ReadTierTagValue(ctx context.Context, tier environment.Tier) (string, error) {
	return c.readTagValue(ctx, tierValue(tier))
}

func (c *Clients) readTagValue(ctx context.Context, asked tagValue) (string, error) {
	c.tierTags.mu.Lock()
	known := c.tierTags.values[asked.short]
	c.tierTags.mu.Unlock()
	if known != "" {
		return known, nil
	}
	service, err := c.GlobalTagService()
	if err != nil {
		return "", err
	}
	path := c.valuePath(asked)
	value, err := googleretry.Attempted(ctx, service.TagValues.GetNamespaced().Name(path).Context(ctx).Do)
	if Absent(err) || Forbidden(err) {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"project %s has no tag value %s that these credentials may read, and %s carries it.\n"+
				"Run `%s`, which makes it, and grant this credential %s on it",
			c.Project(), path, asked.marks, asked.command, TierTagUserRole)
	}
	if err != nil {
		return "", fmt.Errorf("read the tag value %s: %w", path, err)
	}
	c.tierTags.mu.Lock()
	defer c.tierTags.mu.Unlock()
	if c.tierTags.values == nil {
		c.tierTags.values = map[string]string{}
	}
	c.tierTags.values[asked.short] = value.Name
	return value.Name, nil
}

func (c *Clients) EnsureTierTag(ctx context.Context, tier environment.Tier, target TaggedResource) error {
	return c.ensureTag(ctx, tierValue(tier), target)
}

func (c *Clients) EnsureOutsideTag(ctx context.Context, target TaggedResource) error {
	return c.ensureTag(ctx, outsideValue, target)
}

func (c *Clients) ensureTag(ctx context.Context, asked tagValue, target TaggedResource) error {
	if !c.TierTagged() {
		return nil
	}
	value, err := c.readTagValue(ctx, asked)
	if err != nil {
		return err
	}
	service, err := c.tagServiceOf(target)
	if err != nil {
		return err
	}
	var started *crm.Operation
	for attempt := range tierTagSeenAttempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, WaitCeiling) {
			return ctx.Err()
		}
		started, err = googleretry.Attempted(ctx, service.TagBindings.Create(&crm.TagBinding{Parent: target.Name, TagValue: value}).Context(ctx).Do)
		if !Absent(err) {
			break
		}
	}
	switch {
	case err == nil:
		return TagOperationDone(ctx, service, "binding "+c.valuePath(asked)+" to "+target.Name, started)
	case Taken(err):
		return nil
	case googleretry.AnsweredCode(err) == http.StatusBadRequest:
		bound, readErr := c.boundValue(ctx, target)
		switch {
		case readErr != nil:
			return readErr
		case bound.is(value, c.valuePath(asked)):
			return nil
		case bound.binding != "":
			return refusal.Refuse(refusal.CodeInvalid,
				"%s carries %s, another value of the %s tag than %s, and %s carries %s alone.\n"+
					"Remove that binding with `gcloud resource-manager tags bindings delete`, then try again",
				target.Name, bound.named(), c.TierTagKeyPath(), asked.short, asked.marks, c.valuePath(asked))
		}
	}
	return ExplainMissingGrant(fmt.Errorf("bind %s to %s: %w", c.valuePath(asked), target.Name, err),
		c.tagGrant(asked, target, "createTagBinding"))
}

func (c *Clients) HasTierTag(ctx context.Context, tier environment.Tier, target TaggedResource) (bool, error) {
	if !c.TierTagged() {
		return true, nil
	}
	binding, err := c.tagBinding(ctx, tierValue(tier), target)
	return binding != "", err
}

func (c *Clients) CarriesTierTag(ctx context.Context, target TaggedResource) (bool, error) {
	if !c.TierTagged() {
		return true, nil
	}
	bound, err := c.boundValue(ctx, target)
	return bound.binding != "", err
}

func (c *Clients) RemoveTierTag(ctx context.Context, tier environment.Tier, target TaggedResource) error {
	if !c.TierTagged() {
		return nil
	}
	asked := tierValue(tier)
	binding, err := c.tagBinding(ctx, asked, target)
	if binding == "" || err != nil {
		return err
	}
	service, err := c.tagServiceOf(target)
	if err != nil {
		return err
	}
	started, err := googleretry.Attempted(ctx, service.TagBindings.Delete(binding).Context(ctx).Do)
	if Absent(err) {
		return nil
	}
	if err != nil {
		return ExplainMissingGrant(fmt.Errorf("remove %s from %s: %w", c.valuePath(asked), target.Name, err),
			c.tagGrant(asked, target, "deleteTagBinding"))
	}
	return TagOperationDone(ctx, service, "removing "+c.valuePath(asked)+" from "+target.Name, started)
}

func (c *Clients) RemoveTierTagIfFound(ctx context.Context, tier environment.Tier, find func(context.Context) (TaggedResource, bool, error)) error {
	if !c.TierTagged() {
		return nil
	}
	target, found, err := find(ctx)
	if !found || err != nil {
		return err
	}
	return c.RemoveTierTag(ctx, tier, target)
}

func (c *Clients) tagGrant(asked tagValue, target TaggedResource, verb string) string {
	return TierTagUserRole + " on " + c.valuePath(asked) + " and " + target.PermissionPrefix + "." + verb + " on " + target.Name
}

func (c *Clients) tagBinding(ctx context.Context, asked tagValue, target TaggedResource) (string, error) {
	value, err := c.readTagValue(ctx, asked)
	if err != nil {
		return "", err
	}
	bound, err := c.boundValue(ctx, target)
	if err != nil || !bound.is(value, c.valuePath(asked)) {
		return "", err
	}
	return bound.binding, nil
}

type boundTag struct {
	binding string
	value   string
	path    string
}

func (b boundTag) is(value, path string) bool {
	return b.binding != "" && (b.value == value || b.path == path)
}

func (b boundTag) named() string { return cmp.Or(b.path, b.value) }

func (c *Clients) boundValue(ctx context.Context, target TaggedResource) (boundTag, error) {
	service, err := c.tagServiceOf(target)
	if err != nil {
		return boundTag{}, err
	}
	listed, err := googleretry.Attempted(ctx, service.TagBindings.List().Parent(target.Name).Context(ctx).Do)
	if Absent(err) {
		return boundTag{}, nil
	}
	if err != nil {
		return boundTag{}, fmt.Errorf("read the tags bound to %s: %w", target.Name, err)
	}
	for _, binding := range listed.TagBindings {
		path := cmp.Or(c.knownValuePath(binding.TagValue), binding.TagValueNamespacedName)
		if strings.HasPrefix(path, c.TierTagKeyPath()+"/") {
			return boundTag{binding: binding.Name, value: binding.TagValue, path: path}, nil
		}
	}
	return boundTag{}, nil
}

func (c *Clients) knownValuePath(value string) string {
	c.tierTags.mu.Lock()
	defer c.tierTags.mu.Unlock()
	for short, known := range c.tierTags.values {
		if known == value {
			return c.TierTagKeyPath() + "/" + short
		}
	}
	return ""
}

func TagOperationDone(ctx context.Context, service *crm.Service, doing string, started *crm.Operation) error {
	_, err := AwaitOperation(ctx, Patience{Attempts: WaitAttempts, Ceiling: WaitCeiling}, "Resource Manager", doing, started,
		func(op *crm.Operation) OperationOutcome {
			if op == nil {
				return OperationOutcome{Done: true}
			}
			outcome := OperationOutcome{Name: op.Name, Done: op.Done}
			if op.Error != nil {
				outcome.Failure = FailureOf(op.Error.Code, op.Error.Message)
			}
			return outcome
		},
		func(name string) (*crm.Operation, error) {
			return googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*crm.Operation, error) {
				return service.Operations.Get(name).Context(ctx).Do(call...)
			})
		})
	return err
}

func (c *Clients) RunServiceTagged(service string) TaggedResource {
	return TaggedResource{Name: "//run.googleapis.com/" + c.ServicePath(service), Regional: true, PermissionPrefix: "run.services"}
}

func (c *Clients) AccountTagged(uniqueID string) TaggedResource {
	return TaggedResource{Name: "//iam.googleapis.com/projects/" + c.Project() + "/serviceAccounts/" + uniqueID, PermissionPrefix: "iam.serviceAccounts"}
}

func (c *Clients) PubSubTagged(path string) TaggedResource {
	prefix := "pubsub.topics"
	if strings.Contains(path, "/subscriptions/") {
		prefix = "pubsub.subscriptions"
	}
	return TaggedResource{Name: "//pubsub.googleapis.com/" + path, PermissionPrefix: prefix}
}

func (c *Clients) RepositoryTagged(repository string) TaggedResource {
	return TaggedResource{Name: "//artifactregistry.googleapis.com/" + RepositoryPath(c, repository), Regional: true, PermissionPrefix: "artifactregistry.repositories"}
}

func (c *Clients) BucketTagged(bucket string) TaggedResource {
	return TaggedResource{Name: "//storage.googleapis.com/projects/_/buckets/" + bucket, Regional: true, PermissionPrefix: "storage.buckets"}
}

func (c *Clients) SQLInstanceTagged(instance string) TaggedResource {
	return TaggedResource{Name: "//sqladmin.googleapis.com/projects/" + c.Project() + "/instances/" + instance, Regional: true, PermissionPrefix: "cloudsql.instances"}
}

func (c *Clients) DatabaseTagged(database string) TaggedResource {
	return TaggedResource{Name: "//firestore.googleapis.com/projects/" + c.Project() + "/databases/" + database, Regional: true, PermissionPrefix: "datastore.databases"}
}

func (c *Clients) SecretTagged(ctx context.Context, secret string) (TaggedResource, error) {
	number, err := c.ReadProjectNumber(ctx)
	if err != nil {
		return TaggedResource{}, err
	}
	return TaggedResource{Name: "//secretmanager.googleapis.com/projects/" + strconv.FormatInt(number, 10) + "/secrets/" + secret, PermissionPrefix: "secretmanager.secrets"}, nil
}

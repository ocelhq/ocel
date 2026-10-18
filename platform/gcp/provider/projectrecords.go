package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	databaseQuotaMessage   = "maximum number of databases"
	projectDatabasesFilter = "databases/"
)

var projectRecordsPermissions = []string{
	"datastore.databases.create",
	"datastore.databases.get",
	"datastore.databases.getMetadata",
	"datastore.operations.get",
	"cloudkms.cryptoKeys.create",
	"cloudkms.cryptoKeys.get",
}

func newProjectRecordsRole(names names.Names) customRole {
	return customRole{
		id:          names.ProjectRecordsRole(),
		title:       "ocel project records (" + string(names.Namespace()) + ")",
		description: "lets a deploy make the Firestore database and the key a project's values are kept and sealed in, granted on the condition it names them, and nothing else",
		permissions: projectRecordsPermissions,
		driftReason: "it holds other permissions than a deploy needs to make a project's records database and key",
	}
}

func projectRecordsCondition(names names.Names, region string) string {
	ring := "projects/" + names.Project() + "/locations/" + region + "/keyRings/" + names.KeyRing()
	clauses := []string{fmt.Sprintf("resource.name == %q", ring)}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		clauses = append(clauses,
			fmt.Sprintf("resource.name.startsWith(%q)", "projects/"+names.Project()+"/databases/"+names.ProjectDatabasePrefix(tier)),
			fmt.Sprintf("resource.name.startsWith(%q)", ring+"/cryptoKeys/"+ports.ProjectKeyPrefix(tier)))
	}
	return strings.Join(clauses, " || ")
}

func projectRecordsGrant(names names.Names, region string) string {
	return names.ProjectRecordsRolePath() + ", on the condition " + projectRecordsCondition(names, region)
}

func projectDatabaseCondition(c *gcloud.Clients, tier environment.Tier, project string) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + project + " " + string(tier) + " records",
		Expression: fmt.Sprintf("resource.name == %q", databasePath(c, c.ProjectDatabase(tier, project))),
	}
}

func projectKeyCondition(c *gcloud.Clients, tier environment.Tier, project string) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + project + " " + string(tier) + " key",
		Expression: fmt.Sprintf("resource.name == %q", c.Workload().ProjectKeyPath(tier, project)),
	}
}

func tierRecordsCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return &policy.Condition{
		Title: "ocel " + string(c.Namespace()) + " " + string(tier) + " records",
		Expression: fmt.Sprintf("resource.name == %q || resource.name.startsWith(%q)",
			databasePath(c, c.Database()), databasePath(c, c.ProjectDatabasePrefix(tier))),
	}
}

func projectKeysCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " project keys",
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", gcloud.KeyRingPath(c)+"/cryptoKeys/"+ports.ProjectKeyPrefix(tier)),
	}
}

func ensureProjectDatabase(ctx context.Context, c *gcloud.Clients, tier environment.Tier, project string) error {
	if c.Emulated() {
		return nil
	}
	err := bootstrap{clients: c}.makeDatabase(ctx, survey{Project: c.Project(), Region: c.Region}, c.ProjectDatabase(tier, project))
	if googleretry.AnsweredCode(err) == http.StatusTooManyRequests || err != nil && strings.Contains(strings.ToLower(err.Error()), databaseQuotaMessage) {
		return refusal.Refuse(refusal.CodeNotReady,
			"project %s has as many Firestore databases as Firestore allows, and every Ocel project keeps its values in one of its own per tier.\n"+
				"Ask Google support to raise the limit of databases per project, or remove projects this tier no longer runs, then try again", c.Project())
	}
	return gcloud.ExplainMissingGrant(err, projectRecordsGrant(c.Names, c.Region))
}

func ensureProjectKey(ctx context.Context, c *gcloud.Clients, tier environment.Tier, project string) error {
	client, err := c.KMS()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
		return client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:      gcloud.KeyRingPath(c),
			CryptoKeyId: ports.ProjectKey(tier, project),
			CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		})
	})
	if err == nil || status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return gcloud.ExplainMissingGrant(fmt.Errorf("create the key project %s seals its %s values under: %w", project, tier, err), projectRecordsGrant(c.Names, c.Region))
}

func keyRingPolicy(c *gcloud.Clients) policy.Resource {
	ring := gcloud.KeyRingPath(c)
	return policy.Resource{Name: "the " + c.KeyRing() + " key ring", Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		client, err := c.KMS()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: ring,
				Options:  &iampb.GetPolicyOptions{RequestedPolicyVersion: policy.ConditionalVersion},
			})
		})
		if err != nil {
			return nil, nil, gcloud.AbsentWhenNotFound(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = gcloud.ToProtoBindings(bindings)
			held.Version = policy.ConditionalVersion
			_, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
				return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: ring, Policy: held})
			})
			return err
		}
		return gcloud.FromProtoBindings(held.GetBindings()), write, nil
	}}
}

func bindProjectKeyRoles(ctx context.Context, c *gcloud.Clients, tier environment.Tier, member string, roles, wanted []string) (bool, error) {
	condition := projectKeysCondition(c, tier)
	changes := make([]policy.Change, 0, len(roles))
	for _, role := range roles {
		changes = append(changes, gcloud.GrantedOrRevoked(role, member, condition, slices.Contains(wanted, role)))
	}
	changed, err := policy.Edit(ctx, keyRingPolicy(c), policy.Each(changes...))
	if policy.IsAbsent(err) {
		return false, nil
	}
	return changed, err
}

func projectKeyRolesGranted(ctx context.Context, c *gcloud.Clients, tier environment.Tier, member string, roles []string) (bool, error) {
	bindings, err := policy.Read(ctx, keyRingPolicy(c))
	if policy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if !policy.Holds(bindings, role, member, projectKeysCondition(c, tier)) {
			return false, nil
		}
	}
	return true, nil
}

func (b bootstrap) takeProjectRecords(ctx context.Context, tier environment.Tier) error {
	if b.clients.Emulated() {
		return nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	listed, err := googleretry.Attempted(ctx, service.Projects.Databases.List("projects/"+b.clients.Project()).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("list the Firestore databases of project %s: %w", b.clients.Project(), err)
	}
	prefix := b.clients.ProjectDatabasePrefix(tier)
	for _, database := range listed.Databases {
		_, id, _ := strings.Cut(database.Name, projectDatabasesFilter)
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if err := b.takeDatabase(ctx, id); err != nil {
			return err
		}
	}
	return b.takeProjectKeys(ctx, tier)
}

func (b bootstrap) takeProjectKeys(ctx context.Context, tier environment.Tier) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	keys := client.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: gcloud.KeyRingPath(b.clients)})
	prefix := gcloud.KeyRingPath(b.clients) + "/cryptoKeys/" + ports.ProjectKeyPrefix(tier)
	var named []string
	for {
		key, err := keys.Next()
		if err != nil {
			if status.Code(err) == codes.NotFound || errors.Is(err, iterator.Done) {
				break
			}
			return fmt.Errorf("list the keys of the %s ring: %w", b.clients.KeyRing(), err)
		}
		if strings.HasPrefix(key.GetName(), prefix) {
			named = append(named, strings.TrimPrefix(key.GetName(), gcloud.KeyRingPath(b.clients)+"/cryptoKeys/"))
		}
	}
	for _, name := range named {
		if _, err := b.takeKey(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) referencedOwners(ctx context.Context, spec provider.StackSpec) ([]string, error) {
	if len(spec.App.Values.Secrets) == 0 {
		return nil, nil
	}
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	owners, err := store.ReferenceOwners(ctx, variablestore.Scope{Project: spec.Ref.Project, Tier: spec.Ref.Tier})
	if err != nil {
		return nil, err
	}
	environments := []string{""}
	if live := liveEnvironment(spec.Ref); live != "" {
		environments = append(environments, live)
	}
	var referenced []string
	for _, secret := range spec.App.Values.Secrets {
		for _, env := range environments {
			cell := variablestore.Coordinate{Cell: variablestore.Cell{Folder: secret.Folder, Key: secret.Key}, Environment: env}
			if owner := owners[cell]; owner != "" && !slices.Contains(referenced, owner) {
				referenced = append(referenced, owner)
			}
		}
	}
	slices.Sort(referenced)
	return referenced, nil
}

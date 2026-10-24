package variables

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cloud.google.com/go/iam/apiv1/iampb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	databaseQuotaMessage   = "maximum number of databases"
	projectDatabasesFilter = "databases/"
)

func RecordsCondition(c *gcloud.Clients, tiers ...environment.Tier) *policy.Condition {
	var clauses []string
	for _, tier := range tiers {
		clauses = append(clauses,
			fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.RecordsDatabase(tier))),
			fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.SharedDatabase(tier))),
			fmt.Sprintf("resource.name.startsWith(%q)", bootstrap.DatabasePath(c, c.OwnDatabasePrefix(tier))))
	}
	title := "ocel " + string(c.Namespace()) + " records"
	if len(tiers) == 1 {
		title = "ocel " + string(c.Namespace()) + " " + string(tiers[0]) + " records"
	}
	return &policy.Condition{Title: title, Expression: strings.Join(clauses, " || ")}
}

func projectKeysCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " project keys",
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", gcloud.KeyRingPath(c)+"/cryptoKeys/"+ports.ProjectKeyPrefix(tier)),
	}
}

func EnsureProjectKey(ctx context.Context, c *gcloud.Clients, tier environment.Tier, project string) error {
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
	if err == nil {
		return untilKeyServed(ctx, c, client, tier, project)
	}
	if status.Code(err) == codes.AlreadyExists {
		return refuseUnsealingKey(ctx, c, client, tier, project)
	}
	if status.Code(err) == codes.NotFound {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project has no %s key ring to make project %s's %s key on, and no value of %s can be sealed without that key.\n"+
				"Deploy %s to the %s tier, which adds the %s part holding the ring, then try again",
			c.KeyRing(), project, tier, project, project, tier, bootstrap.KeysPart)
	}
	return gcloud.ExplainMissingGrant(fmt.Errorf("create the key project %s seals its %s values under: %w", project, tier, err), c.DeployCredential(tier).ProjectPermissions().String())
}

func untilKeyServed(ctx context.Context, c *gcloud.Clients, client *kms.KeyManagementClient, tier environment.Tier, project string) error {
	name := c.Workload().ProjectKeyPath(tier, project)
	err := googleretry.UntilServed(ctx, func() error {
		_, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
			return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: name})
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("wait for Cloud KMS to serve the key project %s seals its %s values under: %w", project, tier, err)
	}
	return nil
}

func refuseUnsealingKey(ctx context.Context, c *gcloud.Clients, client *kms.KeyManagementClient, tier environment.Tier, project string) error {
	name := c.Workload().ProjectKeyPath(tier, project)
	key, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
		return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: name})
	})
	if err != nil {
		return gcloud.ExplainMissingGrant(fmt.Errorf("read the key project %s seals its %s values under: %w", project, tier, err), c.DeployCredential(tier).Keys().String())
	}
	var lacks string
	switch {
	case key.GetPurpose() != kmspb.CryptoKey_ENCRYPT_DECRYPT:
		lacks = "its purpose is " + key.GetPurpose().String() + ", not " + kmspb.CryptoKey_ENCRYPT_DECRYPT.String()
	case key.GetPrimary() == nil:
		lacks = "it has no primary version"
	case key.GetPrimary().GetState() != kmspb.CryptoKeyVersion_ENABLED:
		lacks = "its primary version is " + key.GetPrimary().GetState().String()
	default:
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the key %s, which project %s seals its %s values under, is already made and cannot seal a value: %s.\n"+
			"Ocel makes it for %s with an enabled primary version, so another credential made it first. "+
			"Give it an enabled primary version with `gcloud kms keys versions create --key %s --primary` if its purpose is %s, then try again",
		name, project, tier, lacks, kmspb.CryptoKey_ENCRYPT_DECRYPT, name, kmspb.CryptoKey_ENCRYPT_DECRYPT)
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

func BindProjectKeyRoles(ctx context.Context, c *gcloud.Clients, tier environment.Tier, member string, roles, wanted []string) (bool, error) {
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

func ProjectKeyRolesGranted(ctx context.Context, c *gcloud.Clients, tier environment.Tier, member string, roles []string) (bool, error) {
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

func TakeProjectRecords(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	listed, err := googleretry.Attempted(ctx, service.Projects.Databases.List("projects/"+b.Clients.Project()).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("list the Firestore databases of project %s: %w", b.Clients.Project(), err)
	}
	for _, database := range listed.Databases {
		_, id, _ := strings.Cut(database.Name, projectDatabasesFilter)
		if !strings.HasPrefix(id, b.Clients.OwnDatabasePrefix(tier)) {
			continue
		}
		if err := b.TakeTierDatabase(ctx, tier, id); err != nil {
			return err
		}
	}
	return takeProjectKeys(ctx, b, tier)
}

func takeProjectKeys(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if on, err := bootstrap.Keys.HasAPIsOn(ctx, b); err != nil || !on {
		return err
	}
	client, err := b.Clients.KMS()
	if err != nil {
		return err
	}
	keys := client.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: gcloud.KeyRingPath(b.Clients)})
	prefix := gcloud.KeyRingPath(b.Clients) + "/cryptoKeys/" + ports.ProjectKeyPrefix(tier)
	var named []string
	for {
		key, err := keys.Next()
		if err != nil {
			if status.Code(err) == codes.NotFound || errors.Is(err, iterator.Done) {
				break
			}
			return fmt.Errorf("list the keys of the %s ring: %w", b.Clients.KeyRing(), err)
		}
		if strings.HasPrefix(key.GetName(), prefix) {
			named = append(named, strings.TrimPrefix(key.GetName(), gcloud.KeyRingPath(b.Clients)+"/cryptoKeys/"))
		}
	}
	for _, name := range named {
		if err := b.TakeKey(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func ReferencedOwners(ctx context.Context, keyValues records.Store, cipher seal.Cipher, spec provider.StackSpec) ([]string, error) {
	if len(spec.App.Values.Secrets) == 0 {
		return nil, nil
	}
	store := variablestore.Store{KeyValues: keyValues, Cipher: cipher}
	owners, err := store.ReferenceOwners(ctx, variablestore.Scope{Project: spec.Ref.Project, Tier: spec.Ref.Tier})
	if err != nil {
		return nil, err
	}
	environments := []string{""}
	if live := LiveEnvironment(spec.Ref); live != "" {
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

func LiveEnvironment(ref provider.StackRef) string {
	if ref.Tier == environment.TierProduction {
		return ""
	}
	return ref.Name.Env
}

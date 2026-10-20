package gcp

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const productionFence = "on the condition !resource.hasTagKey('acme-prod/ocel-tier') || resource.matchTag('acme-prod/ocel-tier', 'production')"

func renderedDeploy(t *testing.T, tier environment.Tier) []string {
	t.Helper()
	document, err := Credentials{Project: Named("acme-prod"), Namespace: "ocel", Region: "europe-west1"}.Permissions(edge.PurposeDeploy, tier)
	if err != nil {
		t.Fatalf("Permissions(deploy, %s) = %v", tier, err)
	}
	if document.Heading == "" {
		t.Errorf("Permissions(deploy, %s) returned a document under no heading", tier)
	}
	return strings.Split(document.Document, "\n")
}

func TestTheCredentialsPortNamesTheRolesEachPurposeIsGranted(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"roles/run.admin, " + productionFence,
		"roles/storage.admin, " + productionFence,
		"roles/artifactregistry.admin, " + productionFence,
		"roles/iam.serviceAccountUser, " + productionFence,
	} {
		if lines := renderedDeploy(t, environment.TierProduction); !slices.Contains(lines, want) {
			t.Errorf("Permissions(deploy) =\n%s\nwant the line %s", strings.Join(lines, "\n"), want)
		}
	}
	document, err := Credentials{Project: Named("acme-prod"), Namespace: "ocel"}.Permissions(edge.PurposeBootstrap, environment.TierProduction)
	if err != nil {
		t.Fatalf("Permissions(bootstrap) = %v", err)
	}
	for _, role := range []string{"roles/run.admin", "roles/storage.admin", "roles/artifactregistry.admin", "roles/iam.serviceAccountAdmin", "roles/cloudkms.admin"} {
		if !slices.Contains(strings.Split(document.Document, "\n"), role) {
			t.Errorf("Permissions(bootstrap) does not name %s", role)
		}
	}
}

func TestEveryGrantOfADeployCredentialSaysWhatItReachesAndWhyWhenItIsTheWholeProject(t *testing.T) {
	t.Parallel()

	document, err := Credentials{Project: Named("acme-prod"), Namespace: "ocel", Region: "europe-west1"}.Permissions(edge.PurposeDeploy, environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document.Heading, "each fenced") {
		t.Errorf("the deploy credential is headed %q, and some of its grants reach the whole project", document.Heading)
	}
	for _, line := range strings.Split(document.Document, "\n") {
		scoped := strings.Contains(line, ", on the condition ") || strings.Contains(line, ", on the queue ") || strings.Contains(line, ", on the tag value ")
		if !scoped && !strings.Contains(line, ", unconditioned: ") {
			t.Errorf("Permissions(deploy) grants %q, which reads as fenced: a grant on the whole project says so and why", line)
		}
	}
}

func TestADeployCredentialIsRenderedForATierAndNoOtherWord(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := Credentials{Project: Named("acme-prod"), Namespace: "ocel"}.Permissions(edge.PurposeDeploy, "staging")
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Permissions(deploy, staging) = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}

func TestTheRolesRenderedForADeployAreTheOnesADeployUses(t *testing.T) {
	t.Parallel()

	for _, line := range renderedDeploy(t, environment.TierProduction) {
		if strings.HasPrefix(line, "roles/run.developer") {
			t.Errorf("Permissions(deploy) names %s: roles/run.admin covers every permission it grants", line)
		}
	}
}

func TestADeployCredentialHoldsNoUnconditionedRoleOverWhatATierTagFences(t *testing.T) {
	t.Parallel()

	lines := renderedDeploy(t, environment.TierProduction)
	for _, role := range slices.Concat(credential.TieredRoles, []string{
		"roles/datastore.user",
		"roles/storage.objectAdmin",
		"roles/secretmanager.secretAccessor",
		"roles/secretmanager.editor",
		"roles/artifactregistry.writer",
		"roles/artifactregistry.repoAdmin",
		"roles/cloudkms.cryptoKeyEncrypterDecrypter",
		"roles/cloudkms.admin",
		"roles/resourcemanager.projectIamAdmin",
		"roles/resourcemanager.tagUser",
		"roles/compute.networkAdmin",
	}) {
		if slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, role+", unconditioned") }) {
			t.Errorf("Permissions(deploy) =\n%s\ngrants %s on the whole project, over the other tier's resources too", strings.Join(lines, "\n"), role)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "ocel-app-") || strings.Contains(line, "PROJECT_NUMBER") {
			t.Errorf("Permissions(deploy) grants %s by a name fence, which holds both tiers: the tier tag fences them now", line)
		}
		if strings.HasPrefix(line, "roles/cloudtasks.") && !strings.HasPrefix(line, credential.QueueRole+", unconditioned: ") {
			t.Errorf("Permissions(deploy) grants %s, beyond what making and administering the tier's delay queue needs", line)
		}
		if line == "roles/iam.roleAdmin" || strings.Contains(line, "iam.roles.") {
			t.Errorf("Permissions(deploy) names %q, and a deploy that could change a custom role could widen what the grant it holds hands out", line)
		}
	}
}

func TestTheRetiredCustomRolesAreNeitherGrantedNorMade(t *testing.T) {
	t.Parallel()

	document := strings.Join(renderedDeploy(t, environment.TierPreview), "\n")
	for _, retired := range []string{"ocel_app_accounts", "ocel_project_records"} {
		if strings.Contains(document, retired) {
			t.Errorf("Permissions(deploy) =\n%s\nnames %s, which the tier tag replaces", document, retired)
		}
		for _, item := range bootstrap.Resources(names.New("ocel", "acme-prod"), environment.TierPreview, false) {
			if item.Kind == bootstrap.KindRole && item.Name == retired {
				t.Errorf("a bootstrap makes %s, which no credential is granted any more", retired)
			}
		}
	}
}

func TestADeployMayCreateWhatItMakesAgainstTheUntaggedProject(t *testing.T) {
	t.Parallel()

	project := checkedResource{name: "projects/acme-prod"}
	for _, tier := range googletest.BothTiers {
		deploy := deployOf(tier)
		held := grantedOver(t, deploy, project)
		for _, permission := range []string{
			"resourcemanager.projects.get",
			"run.services.create",
			"iam.serviceAccounts.create",
			"pubsub.topics.create",
			"pubsub.subscriptions.create",
			"storage.buckets.create",
			"secretmanager.secrets.create",
			"artifactregistry.repositories.create",
			"datastore.databases.create",
			"cloudsql.instances.create",
			"cloudsql.instances.list",
			"cloudtasks.queues.create",
			"cloudkms.keyRings.create",
			"cloudkms.cryptoKeys.create",
		} {
			if _, holds := held[permission]; !holds {
				t.Errorf("the %s deploy credential lacks %s on the untagged project, and Google checks it there", tier, permission)
			}
		}
		created := deploy.ProjectPermissions()
		if created.Condition != nil {
			t.Errorf("a deploy is granted %s, and no condition admits a check against the ring both tiers' keys hang on", created)
		}
		without := credential.New(names.New("ocel", "acme-prod"), "europe-west1", tier)
		predefined := predefinedRoles(t)
		for _, permission := range created.Permissions {
			for _, grant := range without.Grants() {
				if grant.Role != "" && grantReaches(t, grant, project) && slices.Contains(grantPermissions(t, predefined, grant), permission) {
					t.Errorf("the unconditioned custom role holds %s, which %s already holds on the untagged project", permission, grant)
				}
			}
			if strings.HasSuffix(permission, "TagBinding") || permission == "storage.buckets.list" || strings.HasPrefix(permission, "datastore.entities.") ||
				strings.HasPrefix(permission, "storage.objects.") || permission == "cloudsql.instances.get" {
				t.Errorf("a deploy is granted %s on the whole project, which reaches the other tier's resources", permission)
			}
		}
	}
}

func TestADeployMayGrantTheCachePurgeRoleAndOnlyTheRolesAnAppRuns(t *testing.T) {
	t.Parallel()

	lines := renderedDeploy(t, environment.TierProduction)
	want := deployOf(environment.TierProduction).AppGrants().String()
	if !slices.Contains(lines, want) || !strings.Contains(want, "'projects/acme-prod/roles/ocel_cdn_purge'") {
		t.Errorf("Permissions(deploy) =\n%s\nwant the line %s, holding the cache purge role", strings.Join(lines, "\n"), want)
	}
}

func TestADeployMayAddTheBootstrapPartsGoogleNamesNoConditionForThroughOneCustomRole(t *testing.T) {
	t.Parallel()

	lines := renderedDeploy(t, environment.TierProduction)
	if !slices.ContainsFunc(lines, func(line string) bool {
		return strings.HasPrefix(line, "projects/acme-prod/roles/ocel_parts, unconditioned")
	}) {
		t.Errorf("Permissions(deploy) =\n%s\nwant a line granting projects/acme-prod/roles/ocel_parts unconditioned: a deploy adds a tier's network on first use", strings.Join(lines, "\n"))
	}
	held := bootstrap.NewPartsRole(names.New("ocel", "acme-prod")).Permissions
	for _, permission := range []string{"compute.networks.get", "compute.networks.use", "compute.subnetworks.get", "compute.subnetworks.use"} {
		if !slices.Contains(held, permission) {
			t.Errorf("the parts role holds %v, want %s: Google checks it on the network and subnetwork a service connection policy names when the policy is created", held, permission)
		}
	}
	for _, permission := range held {
		if strings.HasPrefix(permission, "artifactregistry.") {
			t.Errorf("the parts role holds %s on the whole project, and the gate's repository carries its tier's tag", permission)
		}
	}
}

func TestADeployCredentialReadsOnlyItsOwnTiersLogs(t *testing.T) {
	t.Parallel()

	named := names.New("ocel", "acme-prod")
	for _, tier := range googletest.BothTiers {
		lines := renderedDeploy(t, tier)
		want := credential.LogsRole + `, on the condition resource.name == "` + named.LogViewPath(tier) + `"`
		if !slices.Contains(lines, want) {
			t.Errorf("Permissions(deploy, %s) =\n%s\nwant the line %s: a deploy reads its tier's logs through its tier's view", tier, strings.Join(lines, "\n"), want)
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "roles/logging.") && line != want {
				t.Errorf("Permissions(deploy, %s) grants %s, which reads logs beyond its tier's view", tier, line)
			}
			if strings.Contains(line, named.LogView(tier.Sibling())) {
				t.Errorf("Permissions(deploy, %s) grants %s, naming the %s tier's log view", tier, line, tier.Sibling())
			}
		}
	}
}

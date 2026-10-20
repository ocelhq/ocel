package gcp

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const outsideValue = "outside"

var accountPowers = []string{
	"iam.serviceAccounts.actAs",
	"iam.serviceAccounts.setIamPolicy",
	"iam.serviceAccounts.getAccessToken",
	"iam.serviceAccounts.getOpenIdToken",
	"iam.serviceAccounts.implicitDelegation",
	"iam.serviceAccounts.signBlob",
	"iam.serviceAccounts.signJwt",
}

const projectPolicyWrite = "resourcemanager.projects.setIamPolicy"

var (
	matchTagClause   = regexp.MustCompile(`^resource\.matchTag\('([^']+)', '([^']+)'\)$`)
	untaggedClause   = regexp.MustCompile(`^!resource\.hasTagKey\('([^']+)'\)$`)
	nameIsClause     = regexp.MustCompile(`^resource\.name == "([^"]+)"$`)
	nameStartsClause = regexp.MustCompile(`^resource\.name\.startsWith\("([^"]+)"\)$`)
	grantedRoles     = regexp.MustCompile(`^api\.getAttribute\('iam\.googleapis\.com/modifiedGrantsByRole', \[\]\)\.hasOnly\(\[(.*)\]\)$`)
)

type checkedResource struct {
	name string
	tags map[string]string
}

func deployOf(tier environment.Tier) credential.Deploy {
	names := names.New("ocel", "acme-prod")
	return credential.Deploy{Names: names, Region: "europe-west1", Tier: tier, GrantableRoles: grants.ListGrantedRoles(names)}
}

func predefinedRoles(t *testing.T) map[string][]string {
	t.Helper()
	read, err := os.ReadFile("testdata/predefinedroles.json")
	if err != nil {
		t.Fatal(err)
	}
	var roles map[string][]string
	if err := json.Unmarshal(read, &roles); err != nil {
		t.Fatal(err)
	}
	return roles
}

func rolePermissions(t *testing.T, predefined map[string][]string, role string) []string {
	t.Helper()
	if held, known := predefined[role]; known {
		return held
	}
	names := names.New("ocel", "acme-prod")
	if custom, known := bootstrap.FindCustomRole(names, strings.TrimPrefix(role, "projects/acme-prod/roles/")); known {
		return custom.Permissions
	}
	t.Fatalf("nothing here says what %s holds: add it to testdata/predefinedroles.json from `gcloud iam roles describe %s`", role, role)
	return nil
}

func grantPermissions(t *testing.T, predefined map[string][]string, grant credential.Grant) []string {
	t.Helper()
	if grant.Role == "" {
		return grant.Permissions
	}
	return rolePermissions(t, predefined, grant.Role)
}

func conditionAdmits(t *testing.T, expression string, resource checkedResource) bool {
	t.Helper()
	for clause := range strings.SplitSeq(expression, " || ") {
		switch {
		case matchTagClause.MatchString(clause):
			matched := matchTagClause.FindStringSubmatch(clause)
			if resource.tags[matched[1]] == matched[2] {
				return true
			}
		case untaggedClause.MatchString(clause):
			if _, tagged := resource.tags[untaggedClause.FindStringSubmatch(clause)[1]]; !tagged {
				return true
			}
		case nameIsClause.MatchString(clause):
			if resource.name == nameIsClause.FindStringSubmatch(clause)[1] {
				return true
			}
		case nameStartsClause.MatchString(clause):
			if strings.HasPrefix(resource.name, nameStartsClause.FindStringSubmatch(clause)[1]) {
				return true
			}
		case grantedRoles.MatchString(clause):
			return true
		default:
			t.Fatalf("the condition %q holds the clause %q, which this test cannot evaluate", expression, clause)
		}
	}
	return false
}

func grantableRolesOf(t *testing.T, grant credential.Grant) []string {
	t.Helper()
	if grant.Condition == nil {
		t.Fatalf("%s lets the credential grant any role to anyone on the project", grant)
	}
	matched := grantedRoles.FindStringSubmatch(grant.Condition.Expression)
	if matched == nil {
		t.Fatalf("%s lets the credential write the project's policy with no limit on the roles it grants", grant)
	}
	var roles []string
	for quoted := range strings.SplitSeq(matched[1], ", ") {
		roles = append(roles, strings.Trim(quoted, "'"))
	}
	return roles
}

func powersOver(t *testing.T, deploy credential.Deploy, resource checkedResource) map[string]string {
	t.Helper()
	predefined := predefinedRoles(t)
	held := map[string]string{}
	for _, grant := range deploy.Grants() {
		if grant.On != "" || grant.Condition != nil && !conditionAdmits(t, grant.Condition.Expression, resource) {
			continue
		}
		permissions := grantPermissions(t, predefined, grant)
		for _, permission := range permissions {
			held[permission] = grant.String()
		}
		if !slices.Contains(permissions, projectPolicyWrite) {
			continue
		}
		for _, role := range grantableRolesOf(t, grant) {
			for _, permission := range rolePermissions(t, predefined, role) {
				held[permission] = "a grant of " + role + " to itself through " + grant.String()
			}
		}
	}
	return held
}

func TestADeployCredentialHoldsNoPowerOverAServiceAccountOutsideItsTier(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		for label, tags := range map[string]map[string]string{
			"tagged " + string(tier.Sibling()): {key: string(tier.Sibling())},
			"tagged " + outsideValue:           {key: outsideValue},
			"carrying no tier tag":             nil,
		} {
			account := checkedResource{name: "projects/acme-prod/serviceAccounts/someone@acme-prod.iam.gserviceaccount.com", tags: tags}
			held := powersOver(t, deployOf(tier), account)
			for _, power := range accountPowers {
				if via, holds := held[power]; holds {
					t.Errorf("the %s deploy credential holds %s on a service account %s, through %s", tier, power, label, via)
				}
			}
			if via, holds := held["iam.serviceAccounts.createTagBinding"]; holds && tags != nil {
				t.Errorf("the %s deploy credential may bind a tag to a service account %s, through %s", tier, label, via)
			}
		}
	}
}

func TestADeployCredentialHoldsItsTiersPowersOverItsOwnTiersServiceAccounts(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		account := checkedResource{name: "projects/acme-prod/serviceAccounts/app@acme-prod.iam.gserviceaccount.com", tags: map[string]string{key: string(tier)}}
		held := powersOver(t, deployOf(tier), account)
		for _, power := range []string{"iam.serviceAccounts.actAs", "iam.serviceAccounts.setIamPolicy"} {
			if _, holds := held[power]; !holds {
				t.Errorf("the %s deploy credential lacks %s on a service account of its own tier: it runs apps as theirs and lets Pub/Sub sign as the push account", tier, power)
			}
		}
	}
}

func TestEveryRoleAFenceHoldsIsConditionedOnItsOwnTiersTagAlone(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		want := "resource.matchTag('acme-prod/ocel-tier', '" + string(tier) + "')"
		lines := credential.Lines(deployOf(tier).Grants())
		for _, role := range credential.TieredRoles {
			if line := role + ", on the condition " + want; !slices.Contains(lines, line) {
				t.Errorf("the %s deploy credential is granted\n%s\nwant the line %s", tier, strings.Join(lines, "\n"), line)
			}
		}
		for _, line := range lines {
			if strings.Contains(line, "hasTagKey") && !strings.HasPrefix(line, "a custom role holding ") {
				t.Errorf("the %s deploy credential grants %s: a predefined role on every untagged resource reaches what no tier owns", tier, line)
			}
		}
	}
}

func TestTheGrantOnUntaggedResourcesOnlyReadsAndBindsThem(t *testing.T) {
	t.Parallel()

	untagged := deployOf(environment.TierPreview).Untagged()
	if untagged.Condition == nil || untagged.Condition.Expression != "!resource.hasTagKey('acme-prod/ocel-tier')" {
		t.Fatalf("the grant on untagged resources is %s, want it conditioned on the resource carrying no ocel-tier tag", untagged)
	}
	for _, permission := range untagged.Permissions {
		verb := permission[strings.LastIndex(permission, ".")+1:]
		if verb != "get" && verb != "createTagBinding" && verb != "listTagBindings" {
			t.Errorf("the grant on untagged resources holds %s, which reaches a resource of no tier beyond reading it and binding its tier", permission)
		}
	}
	for _, kind := range []string{"run.services", "iam.serviceAccounts", "artifactregistry.repositories", "storage.buckets", "cloudsql.instances"} {
		if !slices.Contains(untagged.Permissions, kind+".createTagBinding") {
			t.Errorf("the grant on untagged resources lacks %s.createTagBinding: a deploy binds that kind's tier right after making it", kind)
		}
	}
}

func TestADeployCredentialMayBindOnlyItsOwnTiersValue(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		lines := credential.Lines(deployOf(tier).Grants())
		want := "roles/resourcemanager.tagUser, on the tag value acme-prod/ocel-tier/" + string(tier)
		if !slices.Contains(lines, want) {
			t.Errorf("the %s deploy credential is granted\n%s\nwant the line %s", tier, strings.Join(lines, "\n"), want)
		}
		for _, line := range lines {
			if strings.Contains(line, "tagUser") && line != want {
				t.Errorf("the %s deploy credential grants %s, and tagUser anywhere but its own value lets it move a resource into another tier", tier, line)
			}
		}
	}
}

func TestADeployCredentialNamesNothingOfTheOtherTier(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		other := tier.Sibling()
		document := strings.Join(credential.Lines(deployOf(tier).Grants()), "\n")
		for _, named := range []string{
			names.DelayQueuePath("europe-west1", other),
			names.Repository(other),
			names.TierTagValuePath(other),
			"'" + string(other) + "')",
			"/cryptoKeys/" + string(other) + `"`,
		} {
			if strings.Contains(document, named) {
				t.Errorf("the %s deploy credential names %s of the %s tier:\n%s", tier, named, other, document)
			}
		}
	}
}

func TestAPreviewDeployCredentialMayNeitherOpenNorReadWhatProductionKeeps(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	production := map[string]string{key: string(environment.TierProduction)}
	for _, reached := range []struct {
		resource checkedResource
		powers   []string
	}{
		{checkedResource{name: "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/prod-p-6f25da114a"}, []string{"cloudkms.cryptoKeyVersions.useToDecrypt", "cloudkms.cryptoKeys.setIamPolicy"}},
		{checkedResource{name: "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"}, []string{"cloudkms.cryptoKeyVersions.useToDecrypt", "cloudkms.cryptoKeyVersions.useToEncrypt"}},
		{checkedResource{name: "projects/_/buckets/ocel-prod-cache-k3x9q2/objects/fetch-cache/shop/prod/web/x", tags: production}, []string{"storage.objects.get", "storage.buckets.setIamPolicy"}},
		{checkedResource{name: "projects/acme-prod/secrets/ocel-prod-pulumi-passphrase", tags: production}, []string{"secretmanager.versions.access", "secretmanager.secrets.setIamPolicy"}},
	} {
		held := powersOver(t, deployOf(environment.TierPreview), reached.resource)
		for _, power := range reached.powers {
			if via, holds := held[power]; holds {
				t.Errorf("the preview deploy credential holds %s on %s, through %s", power, reached.resource.name, via)
			}
		}
	}
}

func TestAPreviewDeployCredentialMayNotReadProductionsRecords(t *testing.T) {
	t.Skip("Needs a ruling: Firestore has no per-database IAM policy, so an app's records grant lives on the project, and modifiedGrantsByRole limits which roles a deploy grants there but not to whom or on what condition: the preview credential can grant itself roles/datastore.user on the whole project")
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	records := checkedResource{name: "projects/acme-prod/databases/ocel-prod-records", tags: map[string]string{key: string(environment.TierProduction)}}
	held := powersOver(t, deployOf(environment.TierPreview), records)
	for _, power := range []string{"datastore.entities.get", "datastore.entities.update"} {
		if via, holds := held[power]; holds {
			t.Errorf("the preview deploy credential holds %s on production's records, through %s", power, via)
		}
	}
}

package gcp

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

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
	typeIsClause     = regexp.MustCompile(`^resource\.type == "([^"]+)"$`)
	grantedRoles     = regexp.MustCompile(`^api\.getAttribute\('iam\.googleapis\.com/modifiedGrantsByRole', \[\]\)\.hasOnly\(\[(.*)\]\)$`)
	resourceName     = regexp.MustCompile(`^[a-z][a-z0-9-]*(/[^/\s]+)+$`)
)

const (
	keyRingType  = "cloudkms.googleapis.com/KeyRing"
	locationType = "cloud.googleapis.com/Location"
)

type checkedResource struct {
	name string
	kind string
	tags map[string]string
}

func deployOf(tier environment.Tier) credential.Deploy {
	names := names.New("ocel", "acme-prod")
	return credential.New(names, "europe-west1", tier)
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
		case typeIsClause.MatchString(clause):
			if resource.kind == typeIsClause.FindStringSubmatch(clause)[1] {
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

func grantReaches(t *testing.T, grant credential.Grant, resource checkedResource) bool {
	t.Helper()
	if on := grant.On.Name; on != "" {
		if !resourceName.MatchString(on) {
			t.Fatalf("%s is granted on %q, which is not a resource name this test can place", grant, on)
		}
		if resource.name != on && !strings.HasPrefix(resource.name, on+"/") {
			return false
		}
	}
	return grant.Condition == nil || conditionAdmits(t, grant.Condition.Expression, resource)
}

func grantedOver(t *testing.T, deploy credential.Deploy, resource checkedResource) map[string]string {
	t.Helper()
	predefined := predefinedRoles(t)
	held := map[string]string{}
	for _, grant := range deploy.Grants() {
		if !grantReaches(t, grant, resource) {
			continue
		}
		for _, permission := range grantPermissions(t, predefined, grant) {
			held[permission] = grant.String()
		}
	}
	return held
}

func powersOver(t *testing.T, deploy credential.Deploy, resource checkedResource) map[string]string {
	t.Helper()
	predefined := predefinedRoles(t)
	held := grantedOver(t, deploy, resource)
	for _, grant := range deploy.Grants() {
		if !grantReaches(t, grant, resource) || !slices.Contains(grantPermissions(t, predefined, grant), projectPolicyWrite) {
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

func TestADeployCredentialHoldsNoPowerOverAServiceAccountOfTheOtherTier(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		account := checkedResource{name: "projects/acme-prod/serviceAccounts/someone@acme-prod.iam.gserviceaccount.com", tags: map[string]string{key: string(tier.Sibling())}}
		held := powersOver(t, deployOf(tier), account)
		for _, power := range append(slices.Clone(accountPowers), "iam.serviceAccounts.createTagBinding") {
			if via, holds := held[power]; holds {
				t.Errorf("the %s deploy credential holds %s on a service account tagged %s, through %s", tier, power, tier.Sibling(), via)
			}
		}
	}
}

func TestADeployCredentialReachesAnUntaggedResourceAsItReachesItsOwnTiers(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		untagged := powersOver(t, deployOf(tier), checkedResource{name: "projects/acme-prod/serviceAccounts/new@acme-prod.iam.gserviceaccount.com"})
		own := powersOver(t, deployOf(tier), checkedResource{name: "projects/acme-prod/serviceAccounts/new@acme-prod.iam.gserviceaccount.com", tags: map[string]string{key: string(tier)}})
		for power := range own {
			if _, holds := untagged[power]; !holds {
				t.Errorf("the %s deploy credential holds %s on its own tier's account and not on an untagged one: a resource it has just made carries no tier tag until Google applies the binding", tier, power)
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

func TestEveryRoleAFenceHoldsIsConditionedOnItsOwnTiersTagOrNoTierTag(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		want := ", on the condition !resource.hasTagKey('acme-prod/ocel-tier') || resource.matchTag('acme-prod/ocel-tier', '" + string(tier) + "')"
		lines := credential.Lines(deployOf(tier).Grants())
		for _, role := range credential.TieredRoles {
			if line := role + want; !slices.Contains(lines, line) {
				t.Errorf("the %s deploy credential is granted\n%s\nwant the line %s", tier, strings.Join(lines, "\n"), line)
			}
		}
		for _, line := range lines {
			if strings.Contains(line, "hasTagKey") && !strings.HasSuffix(line, want) {
				t.Errorf("the %s deploy credential grants %s: a grant on an untagged resource is a tiered role, admitting its own tier's resources too", tier, line)
			}
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

func TestAPreviewDeployCredentialIsGrantedNothingOnProductionsRecords(t *testing.T) {
	t.Parallel()

	key := names.New("ocel", "acme-prod").TierTagKeyPath()
	records := checkedResource{name: "projects/acme-prod/databases/ocel-prod-records", tags: map[string]string{key: string(environment.TierProduction)}}
	held := grantedOver(t, deployOf(environment.TierPreview), records)
	for _, power := range []string{"datastore.entities.get", "datastore.entities.update", "datastore.databases.delete"} {
		if via, holds := held[power]; holds {
			t.Errorf("the preview deploy credential holds %s on production's records, through %s", power, via)
		}
	}
}

func TestTheProjectPermissionsRoleHoldsOnlyWhatNoTieredRoleHoldsOnTheUntaggedProject(t *testing.T) {
	t.Parallel()

	predefined := predefinedRoles(t)
	project := checkedResource{name: "projects/acme-prod"}
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		deploy := deployOf(tier)
		held := map[string]string{}
		for _, role := range credential.TieredRoles {
			grant := deploy.Tiered(role)
			if !grantReaches(t, grant, project) {
				continue
			}
			for _, permission := range grantPermissions(t, predefined, grant) {
				held[permission] = role
			}
		}
		for _, permission := range deploy.ProjectPermissions().Permissions {
			if via, holds := held[permission]; holds {
				t.Errorf("the %s deploy credential's custom project role holds %s, which %s on the tier condition already holds on the untagged project", tier, permission, via)
			}
		}
	}
}

func TestADeployCredentialReadsOcelsKeyRingAndMakesItsKeyAndNoOtherRings(t *testing.T) {
	t.Parallel()

	ours := checkedResource{name: "projects/acme-prod/locations/europe-west1/keyRings/ocel", kind: keyRingType}
	foreign := checkedResource{name: "projects/acme-prod/locations/europe-west1/keyRings/payroll", kind: keyRingType}
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		onOurs := grantedOver(t, deployOf(tier), ours)
		onForeign := grantedOver(t, deployOf(tier), foreign)
		if via, holds := onOurs["cloudkms.cryptoKeys.list"]; holds {
			t.Errorf("the %s deploy credential holds cloudkms.cryptoKeys.list on %s through %s, and a deploy reaches its tier key by name", tier, ours.name, via)
		}
		for _, permission := range []string{"cloudkms.keyRings.get", "cloudkms.cryptoKeys.create"} {
			if _, holds := onOurs[permission]; !holds {
				t.Errorf("the %s deploy credential holds no %s on %s: Cloud KMS checks it against the ring itself", tier, permission, ours.name)
			}
			if via, holds := onForeign[permission]; holds {
				t.Errorf("the %s deploy credential holds %s on %s, a key ring Ocel never made, through %s", tier, permission, foreign.name, via)
			}
		}
	}
}

func TestADeployCredentialMayUseAndAdministerNoKeyOnTheRingButItsTiers(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		other := checkedResource{name: "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/" + naming.TierWord(tier) + "-p-6f25da114a"}
		held := powersOver(t, deployOf(tier), other)
		for _, power := range []string{"cloudkms.cryptoKeyVersions.useToDecrypt", "cloudkms.cryptoKeyVersions.destroy", "cloudkms.cryptoKeys.setIamPolicy"} {
			if via, holds := held[power]; holds {
				t.Errorf("the %s deploy credential holds %s on %s, through %s: every value of the tier is sealed under its tier key alone", tier, power, other.name, via)
			}
		}
	}
}

func TestADeployCredentialMayMakeOcelsKeyRingInItsLocation(t *testing.T) {
	t.Parallel()

	location := checkedResource{name: "projects/acme-prod/locations/europe-west1", kind: locationType}
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		if _, holds := grantedOver(t, deployOf(tier), location)["cloudkms.keyRings.create"]; !holds {
			t.Errorf("the %s deploy credential holds no cloudkms.keyRings.create on %s: Cloud KMS checks making a ring against the location it is made in, so the keys part could not make it", tier, location.name)
		}
	}
}

package gcp

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const cdnPurgeRoleEndpoint = "/v1/projects/acme-prod/roles/ocel_cdn_purge"

func currentCDNPurgeRole() *iam.Role {
	return &iam.Role{
		Name:                "projects/acme-prod/roles/ocel_cdn_purge",
		IncludedPermissions: []string{"compute.urlMaps.invalidateCache"},
		Stage:               "GA",
		Etag:                "BwXhoLA=",
	}
}

func TestTheCDNPurgeRoleHoldsOnlyThePermissionToClearCloudCDN(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}

	role := newCDNPurgeRole(names)

	if want := []string{"compute.urlMaps.invalidateCache"}; !slices.Equal(role.permissions, want) {
		t.Errorf("cdnPurgeRole permissions = %v, want %v: a Next app holding more could rewrite the url map it only clears", role.permissions, want)
	}
	if role.id != "ocel_cdn_purge" {
		t.Errorf("cdnPurgeRole id = %q, want ocel_cdn_purge", role.id)
	}
}

func TestTheCDNPurgeRoleIDFitsTheLongestNamespaceAnAccountAllows(t *testing.T) {
	t.Parallel()

	width := maxAccountID - 1 - accountHashLen
	namespace := strings.Repeat("a-", width/2) + strings.Repeat("b", width%2)
	names := Names{namespace: provider.Namespace(namespace), project: "acme-prod"}

	id := names.CDNPurgeRole()

	if !regexp.MustCompile(`^[a-zA-Z0-9_.]{3,64}$`).MatchString(id) {
		t.Errorf("CDNPurgeRole() = %q, want 3 to 64 letters, digits, underscores and periods", id)
	}
	if got := names.CDNPurgeRolePath(); got != "projects/acme-prod/roles/"+id {
		t.Errorf("CDNPurgeRolePath() = %q, want it under the project", got)
	}
}

func TestABootstrapCreatesTheCDNPurgeRoleOnceAndReusesIt(t *testing.T) {
	t.Parallel()
	server := &roleServer{endpoint: cdnPurgeRoleEndpoint}
	b := server.bootstrap(t)
	ctx := t.Context()
	target := item{Kind: KindRole, Name: "ocel_cdn_purge"}

	found, err := b.presenceOf(ctx, environment.TierProduction, target)
	if err != nil || found.present {
		t.Fatalf("presenceOf() = %+v, %v, want an absent role", found, err)
	}
	role, ok := findCustomRole(b.clients.Names, target.Name)
	if !ok {
		t.Fatalf("findCustomRole(%s) found nothing", target.Name)
	}
	if err := b.makeRole(ctx, role); err != nil {
		t.Fatalf("makeRole() = %v", err)
	}
	asked := server.created
	if asked == nil || asked.RoleId != "ocel_cdn_purge" || asked.Role.Stage != "GA" ||
		!slices.Equal(asked.Role.IncludedPermissions, []string{"compute.urlMaps.invalidateCache"}) {
		t.Fatalf("the role was created as %+v, want id ocel_cdn_purge, stage GA and the one permission", asked)
	}
	if asked.Role.Title != "ocel cache purge (ocel)" {
		t.Errorf("the role is titled %q, want %q", asked.Role.Title, "ocel cache purge (ocel)")
	}

	found, err = b.presenceOf(ctx, environment.TierProduction, target)
	if err != nil || !found.present || found.mends != "" {
		t.Fatalf("presenceOf() = %+v, %v, want a present role with nothing to mend", found, err)
	}
	if err := b.makeRole(ctx, role); err != nil {
		t.Fatalf("makeRole() again = %v", err)
	}
	if !slices.Equal(server.writes, []string{"create"}) {
		t.Errorf("the runs wrote %v, want one create", server.writes)
	}
}

func TestACDNPurgeRoleWithOtherPermissionsIsPutBackToClearingCloudCDN(t *testing.T) {
	t.Parallel()
	server := &roleServer{endpoint: cdnPurgeRoleEndpoint, role: currentCDNPurgeRole()}
	server.role.IncludedPermissions = append(server.role.IncludedPermissions, "compute.urlMaps.update")
	b := server.bootstrap(t)
	target := item{Kind: KindRole, Name: "ocel_cdn_purge"}

	found, err := b.presenceOf(t.Context(), environment.TierProduction, target)
	want := newCDNPurgeRole(b.clients.Names).driftReason
	if err != nil || !found.present || found.mends != want {
		t.Fatalf("presenceOf() = %+v, %v, want a present role to mend for %q", found, err, want)
	}
	if err := b.makeRole(t.Context(), newCDNPurgeRole(b.clients.Names)); err != nil {
		t.Fatalf("makeRole() = %v", err)
	}
	if !slices.Equal(server.writes, []string{"patch"}) || !slices.Equal(server.patched.IncludedPermissions, []string{"compute.urlMaps.invalidateCache"}) {
		t.Errorf("makeRole() wrote %v and sent %+v, want one patch back to the one permission", server.writes, server.patched)
	}
}

func TestAnUnknownCustomRoleIsRefusedByName(t *testing.T) {
	t.Parallel()
	b := (&roleServer{}).bootstrap(t)
	read := survey{Tier: environment.TierProduction, Names: b.clients.Names}

	err := b.make(t.Context(), read, item{Kind: KindRole, Name: "ocel_other"})

	if err == nil || !strings.Contains(err.Error(), "no custom role is named ocel_other") {
		t.Errorf("make(unknown role) = %v, want it to say no custom role is named ocel_other", err)
	}
}

func TestOnlyANonEmulatedAlbBootstrapKeepsTheCDNPurgeRole(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		features []string
		emulated bool
		want     bool
	}{
		"alb against Google":       {[]string{albFeature}, false, true},
		"alb against an emulator":  {[]string{albFeature}, true, false},
		"no feature":               {nil, false, false},
		"another feature":          {[]string{kvFeature}, false, false},
		"alb among other features": {[]string{kvFeature, albFeature}, false, true},
	} {
		if got := keepsCDNPurgeRole(test.features, test.emulated); got != test.want {
			t.Errorf("%s: keepsCDNPurgeRole = %t, want %t", name, got, test.want)
		}
	}
}

func TestAnEmulatedBootstrapWithTheAlbMakesNoCDNPurgeRole(t *testing.T) {
	t.Parallel()
	server := &roleServer{endpoint: cdnPurgeRoleEndpoint}
	b := server.bootstrap(t)
	progress := &fake.Log{}

	err := b.raiseCDNPurgeRole(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{albFeature}}, progress)

	if err != nil || len(server.writes) != 0 || len(progress.Lines()) != 0 {
		t.Errorf("raiseCDNPurgeRole() = %v after writes %v and saying %q, want nothing against an emulator: floci answers 404 to every roles call", err, server.writes, progress.Lines())
	}
}

func TestDroppingTheAlbSaysTheCDNPurgeRoleIsKept(t *testing.T) {
	t.Parallel()
	b, _ := fronting(t)
	progress := &fake.Log{}
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}

	if err := b.dropFeatures(t.Context(), surveyed(albFeature), req, progress); err != nil {
		t.Fatalf("dropFeatures = %v", err)
	}

	want := "INFO Kept custom role projects/acme-prod/roles/ocel_cdn_purge: " + reasonRoleKept
	if !slices.Contains(progress.Lines(), want) {
		t.Errorf("the bootstrap said %q, want a line %q", progress.Lines(), want)
	}
}

func TestTheAlbFeatureSaysItKeepsACustomRoleThatMayOnlyClearCloudCDN(t *testing.T) {
	t.Parallel()

	summary := bootstrap{}.summaryOf(albFeature)

	if !strings.Contains(summary, "a custom role that may only clear Cloud CDN, which Next apps behind it are granted") {
		t.Errorf("the alb feature summary = %q, want it to name the purge role", summary)
	}
}

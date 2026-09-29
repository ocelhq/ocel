package providerserver

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const EveryPreview = ""

func buildDeploySpec(req *contractv1.DeployRequest, promotionID string) (provider.DeploySpec, error) {
	manifest := req.GetManifest()
	env := req.GetEnvironment()

	tier, err := decodeTier(env.GetTier())
	if err != nil {
		return provider.DeploySpec{}, err
	}
	name, err := envName(env)
	if err != nil {
		return provider.DeploySpec{}, err
	}
	slug := manifest.GetSlug()
	if slug == "" {
		return provider.DeploySpec{}, refusal.Refuse(refusal.CodeInvalid, "this manifest names no project, and every stack a deploy provisions belongs to one")
	}

	spec := provider.DeploySpec{
		Slug:        slug,
		Tier:        tier,
		Env:         name,
		Label:       env.GetLabel(),
		Pointer:     pointerFor(tier, name),
		PromotionID: promotionID,
		Tag:         req.GetTag(),
		Builds:      make(map[string]string, len(manifest.GetApps())),
	}
	if !ephemeral(env) {
		spec.Infra = naming.InfraStack(name)
	}
	for _, app := range manifest.GetApps() {
		entry, err := appEntry(app, name, promotionID)
		if err != nil {
			return provider.DeploySpec{}, err
		}
		spec.Builds[entry.App] = entry.Build.String()
		spec.Apps = append(spec.Apps, entry)
	}
	return spec, nil
}

func appEntry(app *contractv1.ManifestApp, env, promotionID string) (provider.AppEntry, error) {
	name := app.GetName()
	if name == "" {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid, "this manifest declares an app with no name, and a stack is named after the app it serves")
	}
	if name == naming.InfraApp {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid,
			"app %q uses the name reserved for the environment's infra stack; rename the app", name)
	}
	if err := naming.Validate("app name", name); err != nil {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
	}
	identity, err := provider.NewBuild(app.GetDeploymentId(), promotionID, env, fingerprintVariables(app.GetVariables()))
	if err != nil {
		return provider.AppEntry{}, err
	}
	container := app.GetContainer()
	return provider.AppEntry{
		App:             name,
		Stack:           naming.AppStack(env, name, identity.Release()),
		Build:           identity,
		Manifest:        app,
		Image:           container.GetImage(),
		HealthCheckPath: container.GetHealthCheckPath(),
		Arch:            container.GetArch(),
	}, nil
}

func pointerFor(tier environment.Tier, env string) string {
	if tier == environment.TierProduction {
		return router.DefaultPointer
	}
	return env
}

func ephemeral(env *environmentv1.Environment) bool {
	return env.GetTier() == environmentv1.Tier_TIER_PREVIEW &&
		env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}

func envScope(env *environmentv1.Environment) (string, error) {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW && env.GetIdentity() == "" {
		return EveryPreview, nil
	}
	return envName(env)
}

func bindingEnvironment(p provider.DeploySpec) string {
	if p.Tier == environment.TierProduction {
		return ""
	}
	return p.Env
}

func appCoordinate(p provider.DeploySpec, app string, release naming.Release) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(p.Slug),
		Env:     p.Env,
		App:     app,
		Release: release,
	}
}

func appTags(p provider.DeploySpec, entry provider.AppEntry) map[string]string {
	coordinate := appCoordinate(p, entry.App, entry.Build.Release())
	coordinate.Kind = naming.KindFunction
	return coordinate.Tags(naming.TagValues{
		ManagedBy:  "ocel",
		EnvTier:    p.Tier,
		Deployment: entry.Build.DeploymentID(),
		Promotion:  p.PromotionID,
	})
}

func infraTags(p provider.DeploySpec) map[string]string {
	tags := map[string]string{
		"ocel:managed-by":    "ocel",
		"ocel:project":       naming.Sanitize(p.Slug),
		"ocel:env":           p.Env,
		naming.EnvTierTagKey: string(p.Tier),
		"ocel:stack":         p.Infra.String(),
	}
	return tags
}

func isOfTier(stack naming.StackName, tier environment.Tier) bool {
	return (stack.Env == stackrecords.ProductionEnv) == (tier == environment.TierProduction)
}

func classifyStacks(entries []stackrecords.NamedStack, tier environment.Tier) (infra, apps []naming.StackName, pointers []string) {
	for _, entry := range entries {
		if !isOfTier(entry.Name, tier) {
			continue
		}
		if !slices.Contains(pointers, entry.Name.Env) {
			pointers = append(pointers, entry.Name.Env)
		}
		if entry.Name.IsInfra() {
			infra = append(infra, entry.Name)
			continue
		}
		apps = append(apps, entry.Name)
	}
	slices.Sort(pointers)
	return infra, apps, pointers
}

func fingerprintVariables(variables []*contractv1.ManifestVariable) string {
	if len(variables) == 0 {
		return ""
	}
	ordered := slices.Clone(variables)
	slices.SortFunc(ordered, func(a, b *contractv1.ManifestVariable) int {
		if a.GetFolder() != b.GetFolder() {
			return strings.Compare(a.GetFolder(), b.GetFolder())
		}
		return strings.Compare(a.GetKey(), b.GetKey())
	})
	h := sha256.New()
	for _, variable := range ordered {
		provider.WriteLenPrefixed(h, []byte(variable.GetKey()))
		provider.WriteLenPrefixed(h, []byte(variable.GetFolder()))
		provider.WriteLenPrefixed(h, []byte(strconv.FormatInt(variable.GetVersion(), 10)))
	}
	return hex.EncodeToString(h.Sum(nil))[:provider.FingerprintHexLen]
}

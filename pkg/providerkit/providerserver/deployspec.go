package providerserver

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const EveryPreview = ""

func buildDeploySpec(req *contractv1.DeployRequest, promotionID string) (provider.DeploySpec, error) {
	manifest := req.GetManifest()
	env := req.GetEnvironment()

	class, err := classOf(env.GetTier())
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
		Class:       class,
		Env:         name,
		Label:       env.GetLabel(),
		Pointer:     pointerFor(class, name),
		PromotionID: promotionID,
		Tag:         req.GetTag(),
		Builds:      make(map[string]string, len(manifest.GetApps())),
	}
	if !ephemeral(env) {
		spec.Infra = naming.InfraStack(name)
	}
	containers, err := appContainers(manifest)
	if err != nil {
		return provider.DeploySpec{}, err
	}
	for _, app := range manifest.GetApps() {
		entry, err := appEntry(app, name)
		if err != nil {
			return provider.DeploySpec{}, err
		}
		if container, ours := containers[entry.App]; ours {
			entry.Image = container.GetImage()
			entry.HealthCheckPath = container.GetHealthCheckPath()
			entry.Arch = container.GetArch()
			spec.Builds[entry.App] = entry.Image
		} else {
			spec.Builds[entry.App] = entry.Build.String()
		}
		spec.Apps = append(spec.Apps, entry)
	}
	if err := refuseOrphanFunctions(manifest, spec.Builds); err != nil {
		return provider.DeploySpec{}, err
	}
	return spec, nil
}

func appContainers(manifest *contractv1.Manifest) (map[string]*contractv1.ManifestContainer, error) {
	compute := make(map[string]string, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		compute[app.GetName()] = app.GetCompute()
	}

	containers := make(map[string]*contractv1.ManifestContainer, len(manifest.GetContainers()))
	for _, container := range manifest.GetContainers() {
		app := container.GetApp()
		kind, declared := compute[app]
		if !declared {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a container names the app %q, which this manifest does not declare", app)
		}
		if kind != string(provider.ComputeContainer) {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a container names the app %q, which this manifest says runs on %q compute", app, kind)
		}
		if _, twice := containers[app]; twice {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q declares two containers, and an app is served by one process", app)
		}
		containers[app] = container
	}
	for app, kind := range compute {
		if kind == string(provider.ComputeContainer) && containers[app].GetImage() == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q runs on container compute and this manifest names no image for it", app)
		}
	}
	for _, fn := range manifest.GetFunctions() {
		if _, served := containers[fn.GetApp()]; served {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q runs on container compute and this manifest packs function %s into it as well, so two things would answer the same request",
				fn.GetApp(), fn.GetLogicalName())
		}
	}
	return containers, nil
}

func refuseOrphanFunctions(manifest *contractv1.Manifest, declared map[string]string) error {
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		if app == "" {
			return refusal.Refuse(refusal.CodeInvalid,
				"function %s names no app, and a function ships inside the app that declares it", fn.GetLogicalName())
		}
		if _, ours := declared[app]; !ours {
			return refusal.Refuse(refusal.CodeInvalid,
				"function %s names the app %q, which this manifest does not declare", fn.GetLogicalName(), app)
		}
	}
	return nil
}

func appEntry(app *contractv1.ManifestApp, env string) (provider.AppEntry, error) {
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
	identity, err := provider.NewBuild(app.GetDeploymentId(), env, fingerprintVariables(app.GetVariables()))
	if err != nil {
		return provider.AppEntry{}, err
	}
	return provider.AppEntry{
		App:      name,
		Stack:    naming.AppStack(env, name, identity.Release()),
		Build:    identity,
		Manifest: app,
	}, nil
}

func pointerFor(class edge.Class, env string) string {
	if class == edge.ClassProduction {
		return edge.DefaultPointer
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
	if p.Class == edge.ClassProduction {
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
	return coordinate.Tags(naming.Facts{
		ManagedBy:  "ocel",
		EnvClass:   string(p.Class),
		Deployment: entry.Build.DeploymentID(),
		Promotion:  p.PromotionID,
	})
}

func infraTags(p provider.DeploySpec) map[string]string {
	tags := map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:project":    naming.Sanitize(p.Slug),
		"ocel:env":        p.Env,
		"ocel:env-class":  string(p.Class),
		"ocel:stack":      p.Infra.String(),
	}
	return tags
}

func classifyStacks(entries []stackrecords.NamedStack, class edge.Class) (infra, apps []naming.StackName, pointers []string) {
	for _, entry := range entries {
		production := entry.Name.Env == stackrecords.ProductionEnv
		if production != (class == edge.ClassProduction) {
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

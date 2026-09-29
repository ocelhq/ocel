package providerserver

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/envvarsserver"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type inlineBinding struct {
	record   *bindingsv1.Binding
	site     string
	resource provider.Resource
}

func readInlineBindings(req *contractv1.DeployRequest) ([]inlineBinding, error) {
	resources, err := manifestResources(req.GetManifest())
	if err != nil {
		return nil, err
	}
	inline := make([]inlineBinding, 0, len(req.GetInlineBindings()))
	for _, record := range req.GetInlineBindings() {
		name := record.GetName()
		if err := envvarsserver.ValidateInlineClaim(naming.InlineRecordOwner, name); err != nil {
			return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err)
		}
		if err := envvarsserver.VerifyBinding(record); err != nil {
			return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err)
		}
		if err := envvarsserver.RefuseUnsourced(naming.InlineRecordOwner, record); err != nil {
			return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err)
		}
		bound := slices.IndexFunc(resources, func(resource provider.Resource) bool { return resource.Binding == name })
		if bound < 0 {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"this deploy carries the inline binding record %q, and no resource in its manifest binds it", name)
		}
		inline = append(inline, inlineBinding{
			record:   record,
			site:     "bindings." + strings.TrimPrefix(name, naming.InlineRecordPrefix),
			resource: resources[bound],
		})
	}
	return inline, nil
}

func (b inlineBinding) declaredVersion() string {
	if b.resource.Postgres == nil {
		return ""
	}
	return b.resource.Postgres.Version
}

func (r *deployRun) carriesInline(name string) bool {
	return slices.ContainsFunc(r.inline, func(bound inlineBinding) bool { return bound.record.GetName() == name })
}

func (r *deployRun) checkInlineBindings(ctx context.Context, progress progress.Log) error {
	for _, bound := range r.inline {
		switch {
		case bound.record.GetPostgres() != nil:
			if err := checkInlinePostgres(ctx, bound, progress); err != nil {
				return err
			}
		case bound.record.GetBucket() != nil:
			if err := r.checkInlineBucket(ctx, bound, progress); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkInlinePostgres(ctx context.Context, bound inlineBinding, progress progress.Log) error {
	props := bound.record.GetPostgres()
	served, err := readPostgresVersion(ctx, props)
	if err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"`%s` could not be reached to check it: %s. Ocel checks a database it is bound to rather than provisioning one, so the deploy stops here — check the host, the credentials and that this machine can reach it",
			bound.site, redact(err.Error(), postgresSecrets(props)))
	}
	major := majorOf(served)
	if declared := bound.declaredVersion(); declared != "" && major != declaredMajor(declared) {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s declares postgres %s, and `%s` serves postgres %s: an app written against one major version is not promised the other. Bind a postgres %s server, or declare version %s",
			bound.resource.Declared, declared, bound.site, major, declared, major)
	}
	progress.Detail(fmt.Sprintf("`%s` answered as postgres %s", bound.site, major))
	return nil
}

func (r *deployRun) checkInlineBucket(ctx context.Context, bound inlineBinding, progress progress.Log) error {
	props := bound.record.GetBucket()
	var public bool
	var origins []string
	if spec := bound.resource.Bucket; spec != nil {
		public, origins = spec.Public, spec.AllowedOrigins
	}
	if public && props.GetPublicBaseUrl() == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s is declared public, and `%s` names no publicBaseUrl its objects are served from: add the address the store serves the bucket publicly on",
			bound.resource.Declared, bound.site)
	}
	check := r.provider.Hooks().CheckBucket
	if check == nil {
		return nil
	}
	secrets := []string{props.GetSecretAccessKey()}
	warnings, err := check(ctx, props, public, origins)
	if err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "`%s` was checked and refused: %s", bound.site, redact(err.Error(), secrets))
	}
	for _, warning := range warnings {
		progress.Warn(fmt.Sprintf("`%s`: %s", bound.site, redact(warning, secrets)))
	}
	progress.Detail(fmt.Sprintf("`%s` answered as bucket %s", bound.site, props.GetBucket()))
	return nil
}

func postgresSecrets(props *bindingsv1.PostgresProperties) []string {
	secrets := []string{props.GetPassword(), props.GetUrl()}
	if parsed, err := url.Parse(props.GetUrl()); err == nil && parsed.User != nil {
		if password, set := parsed.User.Password(); set {
			secrets = append(secrets, password, url.QueryEscape(password), url.PathEscape(password))
		}
	}
	return secrets
}

func redact(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func (r *deployRun) publishInlineBindings(ctx context.Context) error {
	versions, err := r.inlineRecordVersions(ctx)
	if err != nil {
		return err
	}
	r.inlineVersions = versions
	if len(r.inline) == 0 {
		return nil
	}
	writes := make([]envvars.NamedBindingWrite, 0, len(r.inline))
	for _, bound := range r.inline {
		pair, err := envvarsserver.BindingPair(naming.InlineRecordOwner, bound.record)
		if err != nil {
			return err
		}
		writes = append(writes, envvars.NamedBindingWrite{Name: bound.record.GetName(), Write: pair})
	}
	if _, err := r.values.SetBindings(ctx, r.scope, bindingEnvironment(r.spec), naming.InlineRecordOwner, writes); err != nil {
		return fmt.Errorf("publish the records %s's inline bindings keep: %w", r.scope.Project, err)
	}
	r.publishedBindings().forget()
	return nil
}

func (r *deployRun) pruneInlineBindings(ctx context.Context, progress progress.Log) {
	current, err := r.inlineRecordVersions(ctx)
	if err != nil {
		progress.Warn(fmt.Sprintf("Could not read the records %s's inline bindings keep, so none was removed: %v. The next deploy removes any no binding keeps", r.scope.Project, err))
		return
	}
	var stale []string
	for name, version := range current {
		if r.carriesInline(name) || r.inlineVersions[name] != version {
			continue
		}
		stale = append(stale, name)
	}
	if len(stale) == 0 {
		return
	}
	slices.Sort(stale)
	if _, err := r.values.RemoveBindings(ctx, r.scope, bindingEnvironment(r.spec), stale); err != nil {
		progress.Warn(fmt.Sprintf("Could not remove %s, which no inline binding keeps any more: %v. The next deploy removes them", strings.Join(stale, ", "), err))
		return
	}
	progress.Detail("Removed " + strings.Join(stale, ", ") + ", which no inline binding keeps any more")
}

func (r *deployRun) inlineRecordVersions(ctx context.Context) (map[string]int64, error) {
	environment := bindingEnvironment(r.spec)
	listed, err := r.values.ListBindings(ctx, r.scope, environment)
	if err != nil {
		return nil, fmt.Errorf("read the records %s's inline bindings keep: %w", r.scope.Project, err)
	}
	versions := map[string]int64{}
	for _, record := range listed {
		if record.Owner == naming.InlineRecordOwner && record.Environment == environment && naming.IsInlineRecord(record.Name) {
			versions[record.Name] = record.Version
		}
	}
	return versions, nil
}

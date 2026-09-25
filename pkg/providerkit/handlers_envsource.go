package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func (h *VarsHandler) syncer(store values.Store, class Class) (*envsource.Syncer, error) {
	vars, err := h.Source.Vars()
	if err != nil {
		return nil, err
	}
	return &envsource.Syncer{Store: store, Class: class, Target: vars.Identity}, nil
}

func (h *VarsHandler) SyncEnvSource(ctx context.Context, req *envvarsv1.SyncEnvSourceRequest) (*envvarsv1.SyncEnvSourceResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	descriptor, resolved := descriptorOf(req.GetEnvSource())
	if descriptor.Kind == envsource.Builtin {
		if err := envsource.Retire(ctx, store, scope.Class, scope.Project); err != nil {
			return nil, RefusalError(err)
		}
		return &envvarsv1.SyncEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: string(envsource.Builtin)}}, nil
	}

	folders := slices.Clone(req.GetFolders())
	if !slices.Contains(folders, "") {
		folders = append(folders, "")
	}
	registration := envsource.Registration{Project: scope.Project, Descriptor: descriptor, Folders: folders}
	if previous, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project); err != nil {
		return nil, RefusalError(err)
	} else if registered && envsource.Identity(ctx, store, scope, previous.Descriptor) != envsource.Identity(ctx, store, scope, descriptor) {
		if err := envsource.Retire(ctx, store, scope.Class, scope.Project); err != nil {
			return nil, RefusalError(err)
		}
	}
	if err := envsource.Register(ctx, store.Records, scope.Class, registration); err != nil {
		return nil, RefusalError(err)
	}
	syncer, err := h.syncer(store, scope.Class)
	if err != nil {
		return nil, err
	}

	var report envsource.Report
	if descriptor.Kind == envsource.Exec {
		report, err = syncer.SyncFrom(ctx, registration, envsource.Static(descriptor.ID(), resolved))
	} else {
		report, err = syncer.Sync(ctx, registration)
	}
	if err != nil {
		return nil, envSourceError(descriptor, err)
	}
	status, err := h.envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	resp := &envvarsv1.SyncEnvSourceResponse{
		Status:  status,
		Written: int32(len(report.Written)),
		Removed: int32(len(report.Removed)),
	}
	for _, at := range report.Present {
		resp.Present = append(resp.Present, &envvarsv1.SourcedCell{Folder: at.Folder, Key: at.Key})
	}
	for at := range report.Refused {
		resp.Refused = append(resp.Refused, &envvarsv1.SourcedCell{Folder: at.Folder, Key: at.Key})
	}
	slices.SortFunc(resp.Refused, func(a, b *envvarsv1.SourcedCell) int {
		return strings.Compare(a.GetFolder()+"/"+a.GetKey(), b.GetFolder()+"/"+b.GetKey())
	})
	return resp, nil
}

func (h *VarsHandler) DescribeEnvSource(ctx context.Context, req *envvarsv1.DescribeEnvSourceRequest) (*envvarsv1.DescribeEnvSourceResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, RefusalError(err)
	}
	if !registered {
		return &envvarsv1.DescribeEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: string(envsource.Builtin)}}, nil
	}
	status, err := h.envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	return &envvarsv1.DescribeEnvSourceResponse{Status: status}, nil
}

func (h *VarsHandler) PutEnvSourceValue(ctx context.Context, req *envvarsv1.PutEnvSourceValueRequest) (*envvarsv1.PutEnvSourceValueResponse, error) {
	at := req.GetCoordinate()
	if at.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a value for preview environment %q is ocel's own to hold, never the env source's: set it with `ocel env set %s --env %s`", at.GetEnvironment(), at.GetKey(), at.GetEnvironment()))
	}
	store, scope, err := h.scoped(req.GetTier(), at.GetSlug())
	if err != nil {
		return nil, err
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, RefusalError(err)
	}
	if !registered || !registration.Descriptor.Writable() {
		source := string(envsource.Builtin)
		if registered {
			source = registration.Descriptor.ID()
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s in %s reads from %s, which ocel may not write into: set write to \"missing\" on the tier's env source to let ocel create a key it lacks", at.GetKey(), scope.Class, source))
	}
	syncer, err := h.syncer(store, scope.Class)
	if err != nil {
		return nil, err
	}
	source, err := syncer.Source(ctx, registration)
	if err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	cell := values.Cell{Folder: at.GetFolder(), Key: at.GetKey()}
	err = source.Put(ctx, cell, []byte(req.GetValue()), req.GetDescription())
	switch {
	case errors.Is(err, envsource.ErrAwaitingApproval):
		return &envvarsv1.PutEnvSourceValueResponse{AwaitingApproval: true}, nil
	case errors.Is(err, envsource.ErrExists):
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
			"%s already holds %s, and ocel never overwrites a value there: change it in %s%s", source.ID(), at.GetKey(), source.ID(), linked(source.Link(cell))))
	case err != nil:
		return nil, envSourceError(registration.Descriptor, err)
	}
	if _, err := syncer.Sync(ctx, registration); err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	value, err := store.Get(ctx, scope, values.Coordinate{Cell: cell}, false)
	if errors.Is(err, values.ErrNotFound) {
		return &envvarsv1.PutEnvSourceValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.PutEnvSourceValueResponse{Metadata: metadataProto(scope, value.Metadata)}, nil
}

func (h *VarsHandler) envSourceStatus(ctx context.Context, store values.Store, scope values.Scope, registration envsource.Registration) (*envvarsv1.EnvSourceStatus, error) {
	status, err := envsource.StatusOf(ctx, store, scope.Class, registration)
	if err != nil {
		return nil, RefusalError(err)
	}
	out := &envvarsv1.EnvSourceStatus{
		EnvSource:     registration.Descriptor.ID(),
		Standing:      registration.Descriptor.Standing(),
		Writable:      registration.Descriptor.Writable(),
		LastAttemptAt: status.LastAttemptAt,
		LastSuccessAt: status.LastSuccessAt,
		LastError:     status.LastError,
	}
	for _, folder := range slices.Sorted(mapKeysOf(status.Links)) {
		out.Links = append(out.Links, &envvarsv1.FolderLink{Folder: folder, Link: status.Links[folder]})
	}
	for _, credential := range registration.Credentials() {
		out.Credentials = append(out.Credentials, credential.Key)
	}
	return out, nil
}

func (h *VarsHandler) refuseSourceOwned(ctx context.Context, store values.Store, scope values.Scope, at values.Coordinate, removing bool) error {
	if at.Environment != "" {
		return nil
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return RefusalError(err)
	}
	if !registered || slices.Contains(registration.Credentials(), at.Cell) {
		return nil
	}
	owner := registration.Descriptor.ID()
	if removing {
		held, err := store.Get(ctx, scope, at, false)
		if errors.Is(err, values.ErrNotFound) {
			return nil
		}
		if err != nil {
			return valuesError(err)
		}
		if held.Provenance.EnvSource != owner {
			return nil
		}
	}
	status, _ := envsource.StatusOf(ctx, store, scope.Class, registration)
	elsewhere := ""
	if scope.Class == ClassPreview {
		elsewhere = " A value for one preview environment stays yours: pass --env <name>."
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s is read from %s, which owns every value %s sets for all of %s: change it there%s, and ocel picks it up on its next sync.%s",
		at, owner, scope.Project, scope.Class, linked(status.Links[at.Folder]), elsewhere))
}

func linked(link string) string {
	if link == "" {
		return ""
	}
	return " (" + link + ")"
}

func mapKeysOf[V any](held map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range held {
			if !yield(key) {
				return
			}
		}
	}
}

func envSourceError(descriptor envsource.Descriptor, err error) error {
	var credential *envsource.CredentialError
	if errors.As(err, &credential) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s signs in with %s, which %s", descriptor.ID(), credential.Var, credential.Reason))
	}
	if _, refused := RefusedCode(err); refused {
		return RefusalError(err)
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("read %s: %w", descriptor.ID(), err))
}

func descriptorOf(wire *envvarsv1.EnvSource) (envsource.Descriptor, map[values.Cell]envsource.Resolved) {
	switch {
	case wire.GetInfisical() != nil:
		held := wire.GetInfisical()
		auth := envsource.InfisicalAuth{}
		switch {
		case held.GetAuth().GetUniversal() != nil:
			universal := held.GetAuth().GetUniversal()
			auth = envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: universal.GetClientIdVar(), ClientSecretVar: universal.GetClientSecretVar()}
		case held.GetAuth().GetAws() != nil:
			auth = envsource.InfisicalAuth{Method: envsource.AuthAWS, IdentityID: held.GetAuth().GetAws().GetIdentityId()}
		case held.GetAuth().GetGcp() != nil:
			auth = envsource.InfisicalAuth{Method: envsource.AuthGCP, IdentityID: held.GetAuth().GetGcp().GetIdentityId()}
		}
		write := envsource.WriteNever
		if held.GetWriteMissing() {
			write = envsource.WriteMissing
		}
		options := envsource.InfisicalOptions{
			Project:     held.GetProject(),
			Environment: held.GetEnvironment(),
			Path:        held.GetPath(),
			Host:        held.GetHost(),
			Auth:        auth,
			Write:       write,
		}.Normalized()
		return envsource.Descriptor{Kind: envsource.Infisical, Infisical: &options}, nil
	case wire.GetExec() != nil:
		held := wire.GetExec()
		resolved := make(map[values.Cell]envsource.Resolved, len(held.GetValues()))
		for _, value := range held.GetValues() {
			resolved[values.Cell{Folder: value.GetFolder(), Key: value.GetKey()}] = envsource.Resolved{Value: []byte(value.GetValue()), Version: value.GetVersion()}
		}
		return envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: held.GetCommand()}}, resolved
	}
	return envsource.Descriptor{Kind: envsource.Builtin}, nil
}

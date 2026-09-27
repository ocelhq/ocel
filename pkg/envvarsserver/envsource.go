package envvarsserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourcewire"
	"github.com/ocelhq/ocel/pkg/envvars"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *Service) envSourceSync(store envvars.Store, class edge.Class) (*envsource.Sync, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return nil, err
	}
	return &envsource.Sync{
		Store: store,
		Class: class,
		Login: envsource.Login{ProveIdentity: backend.ProveIdentity},
	}, nil
}

func (h *Service) SyncEnvSource(ctx context.Context, req *envvarsv1.SyncEnvSourceRequest) (*envvarsv1.SyncEnvSourceResponse, error) {
	if req.GetRegistered() == nil && !h.CallerNamesEnvSource {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(
			"this caller syncs only the env source a deploy registered, never one it names: sync the registered one, or change the tier's envSource and deploy"))
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	if req.GetRegistered() != nil {
		return h.syncRegistered(ctx, store, scope)
	}
	descriptor, read := envsourcewire.Decode(req.GetEnvSource())
	if descriptor.Kind == envsource.Builtin {
		if err := switchToBuiltin(ctx, store, scope); err != nil {
			return nil, provider.RefusalError(err)
		}
		return &envvarsv1.SyncEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: descriptor.ID()}}, nil
	}
	envSync, err := h.envSourceSync(store, scope.Class)
	if err != nil {
		return nil, err
	}
	if err := refuseAuthWithoutLogin(descriptor, envSync.Login); err != nil {
		return nil, err
	}

	folders := req.GetFolders()
	if !slices.Contains(folders, "") {
		folders = append(slices.Clone(folders), "")
	}
	previous, wasRegistered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	registration, err := envsource.Register(ctx, store, scope.Class, envsource.Registration{Project: scope.Project, Descriptor: descriptor, Folders: folders})
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	var copied envsource.CopyResult
	if descriptor.Kind == envsource.Exec {
		copied, err = envSync.CopyProjectFrom(ctx, registration, envsource.NewFixed(descriptor.ID(), read))
	} else {
		copied, err = envSync.CopyProject(ctx, registration)
	}
	if err != nil {
		if len(credentialErrors(err)) == 0 {
			var restored *envsource.Registration
			if wasRegistered {
				restored = &previous
			}
			if restoreErr := envsource.RestoreRegistration(ctx, store, scope.Class, registration, restored); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore the env source this deploy replaced, so the next sync reads %s instead: %w", descriptor.ID(), restoreErr))
			}
		}
		return nil, envSourceError(descriptor, err)
	}
	return syncResponse(ctx, store, scope, registration, copied)
}

func switchToBuiltin(ctx context.Context, store envvars.Store, scope envvars.Scope) error {
	_, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil || !registered {
		return err
	}
	if err := envsource.ClearProvenance(ctx, store, scope); err != nil {
		return err
	}
	return envsource.ForgetProject(ctx, store, scope.Class, scope.Project)
}

func (h *Service) syncRegistered(ctx context.Context, store envvars.Store, scope envvars.Scope) (*envvarsv1.SyncEnvSourceResponse, error) {
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered {
		builtin := envsource.Descriptor{Kind: envsource.Builtin}
		return &envvarsv1.SyncEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: builtin.ID()}}, nil
	}
	if registration.Descriptor.Kind == envsource.Exec {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s in %s reads from exec, whose command runs where ocel deploys: deploy again to read it", scope.Project, scope.Class))
	}
	envSync, err := h.envSourceSync(store, scope.Class)
	if err != nil {
		return nil, err
	}
	if err := refuseAuthWithoutLogin(registration.Descriptor, envSync.Login); err != nil {
		return nil, err
	}
	copied, err := envSync.CopyProject(ctx, registration)
	if err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	return syncResponse(ctx, store, scope, registration, copied)
}

func syncResponse(ctx context.Context, store envvars.Store, scope envvars.Scope, registration envsource.Registration, copied envsource.CopyResult) (*envvarsv1.SyncEnvSourceResponse, error) {
	status, err := envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	resp := &envvarsv1.SyncEnvSourceResponse{
		Status:  status,
		Written: int32(len(copied.Written)),
		Removed: int32(len(copied.Removed)),
	}
	for _, at := range copied.Present {
		resp.Present = append(resp.Present, cellProto(at))
	}
	for _, at := range slices.SortedFunc(maps.Keys(copied.Refused), envvars.Cell.Compare) {
		resp.Refused = append(resp.Refused, &envvarsv1.RefusedCell{Cell: cellProto(at), Reason: copied.Refused[at]})
	}
	return resp, nil
}

func (h *Service) DescribeEnvSource(ctx context.Context, req *envvarsv1.DescribeEnvSourceRequest) (*envvarsv1.DescribeEnvSourceResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered {
		builtin := envsource.Descriptor{Kind: envsource.Builtin}
		return &envvarsv1.DescribeEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: builtin.ID()}}, nil
	}
	status, err := envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	return &envvarsv1.DescribeEnvSourceResponse{Status: status}, nil
}

func (h *Service) CreateEnvSourceValue(ctx context.Context, req *envvarsv1.CreateEnvSourceValueRequest) (*envvarsv1.CreateEnvSourceValueResponse, error) {
	at := req.GetCoordinate()
	if at.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a value for preview environment %q is stored by ocel, never by the env source: set it with `ocel env set %s=<VALUE> --preview --environment %s`",
			at.GetEnvironment(), at.GetKey(), at.GetEnvironment()))
	}
	store, scope, err := h.scoped(req.GetTier(), at.GetSlug())
	if err != nil {
		return nil, err
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered || !registration.Descriptor.CanWrite() {
		current := envsource.Descriptor{Kind: envsource.Builtin}
		if registered {
			current = registration.Descriptor
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s in %s reads from %s, which ocel may not write into: set write to \"missing\" on the tier's env source to let ocel create a key it lacks",
			at.GetKey(), scope.Class, current.ID()))
	}
	cell := envvars.Cell{Folder: at.GetFolder(), Key: at.GetKey()}
	if !slices.Contains(registration.Folders, cell.Folder) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s is not a folder %s reads from %s in %s: ocel creates a value only in the root or in the folder of one of %s's apps",
			cell.Folder, scope.Project, registration.Descriptor.ID(), scope.Class, scope.Project))
	}
	if slices.Contains(registration.Credentials(), cell) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s is what ocel logs in to %s with, so ocel stores it itself: set it with `%s`",
			at.GetKey(), registration.Descriptor.ID(), envsource.SetCommand(scope.Class, at.GetKey())))
	}
	envSync, err := h.envSourceSync(store, scope.Class)
	if err != nil {
		return nil, err
	}
	if err := refuseAuthWithoutLogin(registration.Descriptor, envSync.Login); err != nil {
		return nil, err
	}
	source, err := envSync.Open(ctx, registration)
	if err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	err = source.Create(ctx, cell, []byte(req.GetValue()), req.GetDescription())
	switch {
	case errors.Is(err, envsource.ErrAwaitingApproval):
		return &envvarsv1.CreateEnvSourceValueResponse{AwaitingApproval: true}, nil
	case errors.Is(err, envsource.ErrExists):
		status, _ := envsource.StatusOf(ctx, store, scope.Class, registration)
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
			"%s already has %s, and ocel never overwrites a value there: change it in %s%s",
			source.ID(), at.GetKey(), source.ID(), parenthesized(status.URLs[cell.Folder])))
	case err != nil:
		return nil, envSourceError(registration.Descriptor, err)
	}
	if _, err := envSync.CopyProject(ctx, registration); err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	value, err := store.Get(ctx, scope, envvars.Coordinate{Cell: cell}, false)
	if errors.Is(err, envvars.ErrNotFound) {
		return &envvarsv1.CreateEnvSourceValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.CreateEnvSourceValueResponse{Metadata: metadataProto(scope, value.Metadata)}, nil
}

func envSourceStatus(ctx context.Context, store envvars.Store, scope envvars.Scope, registration envsource.Registration) (*envvarsv1.EnvSourceStatus, error) {
	status, err := envsource.StatusOf(ctx, store, scope.Class, registration)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	out := &envvarsv1.EnvSourceStatus{
		EnvSource:     registration.Descriptor.ID(),
		Scheduled:     registration.Descriptor.IsScheduled(),
		Writable:      registration.Descriptor.CanWrite(),
		LastAttemptAt: unixSeconds(status.LastAttemptAt),
		LastSuccessAt: unixSeconds(status.LastSuccessAt),
		LastError:     status.LastError,
	}
	for _, folder := range slices.Sorted(maps.Keys(status.URLs)) {
		out.Links = append(out.Links, &envvarsv1.FolderLink{Folder: folder, Url: status.URLs[folder]})
	}
	for _, credential := range registration.Credentials() {
		out.Credentials = append(out.Credentials, credential.Key)
	}
	return out, nil
}

func refuseEnvSourceOwned(ctx context.Context, store envvars.Store, scope envvars.Scope, at envvars.Coordinate, removing bool) error {
	if at.Environment != "" {
		return nil
	}
	registration, registered, err := envsource.Registered(ctx, store.Records, scope.Class, scope.Project)
	if err != nil {
		return provider.RefusalError(err)
	}
	if !registered || slices.Contains(registration.Credentials(), at.Cell) {
		return nil
	}
	owner := registration.Descriptor.ID()
	if removing {
		stored, err := store.Get(ctx, scope, at, false)
		if errors.Is(err, envvars.ErrNotFound) {
			return nil
		}
		if err != nil {
			return valuesError(err)
		}
		if stored.Provenance.EnvSource != owner {
			return nil
		}
	}
	status, err := envsource.StatusOf(ctx, store, scope.Class, registration)
	if err != nil {
		return provider.RefusalError(err)
	}
	copiedOn := "the next deploy"
	if registration.Descriptor.IsScheduled() {
		copiedOn = "its next sync, within a minute"
	}
	perEnvironment := ""
	if scope.Class == edge.ClassPreview {
		perEnvironment = " A value for one preview environment is still yours to set with --environment <name>."
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s is read from %s, which owns every value %s sets for all of %s: change it there%s, and ocel copies it on %s.%s",
		at, owner, scope.Project, scope.Class, parenthesized(status.URLs[at.Folder]), copiedOn, perEnvironment))
}

func refuseAuthWithoutLogin(descriptor envsource.Descriptor, login envsource.Login) error {
	if descriptor.Infisical == nil || descriptor.Infisical.Auth.Method != envsource.AuthIdentity || login.ProveIdentity != nil {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s logs in with identity auth, which proves this target's cloud identity, and this target has none: log in with universal auth (clientId and clientSecret) instead",
		descriptor.ID()))
}

func envSourceError(descriptor envsource.Descriptor, err error) error {
	if refused := credentialErrors(err); len(refused) > 0 {
		messages := make([]string, 0, len(refused))
		for _, credential := range refused {
			messages = append(messages, fmt.Sprintf("%s logs in with %s, which %s", descriptor.ID(), credential.Variable, credential.Reason))
		}
		wire := connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.Join(messages, "\n")))
		for _, credential := range refused {
			if detail, err := connect.NewErrorDetail(&envvarsv1.CredentialRefusal{Variable: credential.Variable, Unset: credential.Unset, Reason: credential.Reason}); err == nil {
				wire.AddDetail(detail)
			}
		}
		return wire
	}
	if _, refused := provider.RefusedCode(err); refused {
		return provider.RefusalError(err)
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %w", descriptor.ID(), err))
}

func credentialErrors(err error) []*envsource.CredentialError {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var out []*envsource.CredentialError
		for _, inner := range joined.Unwrap() {
			out = append(out, credentialErrors(inner)...)
		}
		return out
	}
	var credential *envsource.CredentialError
	if errors.As(err, &credential) {
		return []*envsource.CredentialError{credential}
	}
	return nil
}

func cellProto(at envvars.Cell) *envvarsv1.Cell {
	return &envvarsv1.Cell{Folder: at.Folder, Key: at.Key}
}

func unixSeconds(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.Unix()
}

func parenthesized(url string) string {
	if url == "" {
		return ""
	}
	return " (" + url + ")"
}

package variablestoreserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourceproto"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) envSourceSync(store variablestore.Store, tier environment.Tier) (*envsource.Sync, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return nil, err
	}
	return &envsource.Sync{
		Store: store,
		Tier:  tier,
		Login: envsource.Login{ProveIdentity: backend.ProveIdentity},
	}, nil
}

func (h *Service) SyncEnvSource(ctx context.Context, req *variablestorev1.SyncEnvSourceRequest) (*variablestorev1.SyncEnvSourceResponse, error) {
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
	descriptor, read := envsourceproto.Decode(req.GetEnvSource())
	if descriptor.Kind == envsource.Builtin {
		if err := switchToBuiltin(ctx, store, scope); err != nil {
			return nil, provider.RefusalError(err)
		}
		return &variablestorev1.SyncEnvSourceResponse{Status: &variablestorev1.EnvSourceStatus{EnvSource: descriptor.ID()}}, nil
	}
	envSync, err := h.envSourceSync(store, scope.Tier)
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
	previous, wasRegistered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	registration, err := envsource.Register(ctx, store, scope.Tier, envsource.Registration{Project: scope.Project, Descriptor: descriptor, Folders: folders})
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
			if restoreErr := envsource.RestoreRegistration(ctx, store, scope.Tier, registration, restored); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore the env source this deploy replaced, so the next sync reads %s instead: %w", descriptor.ID(), restoreErr))
			}
		}
		return nil, envSourceError(descriptor, err)
	}
	return syncResponse(ctx, store, scope, registration, copied)
}

func switchToBuiltin(ctx context.Context, store variablestore.Store, scope variablestore.Scope) error {
	_, registered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil || !registered {
		return err
	}
	if err := envsource.ClearProvenance(ctx, store, scope); err != nil {
		return err
	}
	return envsource.ForgetProject(ctx, store, scope.Tier, scope.Project)
}

func (h *Service) syncRegistered(ctx context.Context, store variablestore.Store, scope variablestore.Scope) (*variablestorev1.SyncEnvSourceResponse, error) {
	registration, registered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered {
		builtin := envsource.Descriptor{Kind: envsource.Builtin}
		return &variablestorev1.SyncEnvSourceResponse{Status: &variablestorev1.EnvSourceStatus{EnvSource: builtin.ID()}}, nil
	}
	if registration.Descriptor.Kind == envsource.Exec {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s in %s reads from exec, whose command runs where ocel deploys: deploy again to read it", scope.Project, scope.Tier))
	}
	envSync, err := h.envSourceSync(store, scope.Tier)
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

func syncResponse(ctx context.Context, store variablestore.Store, scope variablestore.Scope, registration envsource.Registration, copied envsource.CopyResult) (*variablestorev1.SyncEnvSourceResponse, error) {
	status, err := envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	resp := &variablestorev1.SyncEnvSourceResponse{
		Status:  status,
		Written: int32(len(copied.Written)),
		Removed: int32(len(copied.Removed)),
	}
	for _, at := range copied.Present {
		resp.Present = append(resp.Present, cellProto(at))
	}
	for _, at := range slices.SortedFunc(maps.Keys(copied.Refused), variablestore.Cell.Compare) {
		resp.Refused = append(resp.Refused, &variablestorev1.RefusedCell{Cell: cellProto(at), Reason: copied.Refused[at]})
	}
	return resp, nil
}

func (h *Service) DescribeEnvSource(ctx context.Context, req *variablestorev1.DescribeEnvSourceRequest) (*variablestorev1.DescribeEnvSourceResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	registration, registered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered {
		builtin := envsource.Descriptor{Kind: envsource.Builtin}
		return &variablestorev1.DescribeEnvSourceResponse{Status: &variablestorev1.EnvSourceStatus{EnvSource: builtin.ID()}}, nil
	}
	status, err := envSourceStatus(ctx, store, scope, registration)
	if err != nil {
		return nil, err
	}
	return &variablestorev1.DescribeEnvSourceResponse{Status: status}, nil
}

func (h *Service) SetEnvSourceValue(ctx context.Context, req *variablestorev1.SetEnvSourceValueRequest) (*variablestorev1.SetEnvSourceValueResponse, error) {
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
	registration, registered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if !registered || !registration.Descriptor.CanCreate() {
		current := envsource.Descriptor{Kind: envsource.Builtin}
		if registered {
			current = registration.Descriptor
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s in %s reads from %s, which ocel may not write into: set write to \"values\" on the tier's env source to let ocel create and update its values, or to \"missing\" to let it create a key it lacks",
			at.GetKey(), scope.Tier, current.ID()))
	}
	cell := variablestore.Cell{Folder: at.GetFolder(), Key: at.GetKey()}
	if !slices.Contains(registration.Folders, cell.Folder) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s is not a folder %s reads from %s in %s: ocel writes a value only in the root or in the folder of one of %s's apps",
			cell.Folder, scope.Project, registration.Descriptor.ID(), scope.Tier, scope.Project))
	}
	if slices.Contains(registration.Credentials(), cell) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s is what ocel logs in to %s with, so ocel stores it itself: set it with `%s`",
			at.GetKey(), registration.Descriptor.ID(), envsource.SetCommand(scope.Tier, at.GetKey())))
	}
	copied, err := copiedFrom(ctx, store, scope, cell, registration.Descriptor.ID())
	if err != nil {
		return nil, err
	}
	envSync, err := h.envSourceSync(store, scope.Tier)
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
	url := func() string {
		status, _ := envsource.StatusOf(ctx, store, scope.Tier, registration)
		return parenthesized(status.URLs[cell.Folder])
	}
	creating := copied == nil
	switch {
	case creating:
		err = source.Create(ctx, cell, []byte(req.GetValue()), req.GetDescription())
		if errors.Is(err, envsource.ErrExists) && registration.Descriptor.CanUpdate() {
			creating = false
			err = source.Update(ctx, cell, []byte(req.GetValue()), "")
		}
	case registration.Descriptor.CanUpdate():
		err = source.Update(ctx, cell, []byte(req.GetValue()), copied.Provenance.Version)
	default:
		return nil, refuseOverwrite(source.ID(), at.GetKey(), url())
	}
	switch {
	case errors.Is(err, envsource.ErrAwaitingApproval):
		return &variablestorev1.SetEnvSourceValueResponse{AwaitingApproval: true, Created: creating}, nil
	case errors.Is(err, envsource.ErrNotInFolder):
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s no longer keeps %s in %s itself, so ocel wrote nothing: if it was deleted there, run `%s` and set it again; if it is imported from another folder, change it where it lives%s",
			source.ID(), at.GetKey(), folderName(cell.Folder), syncCommand(scope.Tier), url()))
	case errors.Is(err, envsource.ErrChangedSinceRead):
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf(
			"%s changed in %s since ocel last read it, so ocel wrote nothing over it: run `%s` to read it, then set it again",
			at.GetKey(), source.ID(), syncCommand(scope.Tier)))
	case errors.Is(err, envsource.ErrExists):
		return nil, refuseOverwrite(source.ID(), at.GetKey(), url())
	case err != nil:
		return nil, envSourceError(registration.Descriptor, err)
	}
	if _, err := envSync.CopyProject(ctx, registration); err != nil {
		return nil, envSourceError(registration.Descriptor, err)
	}
	value, err := store.Get(ctx, scope, variablestore.Coordinate{Cell: cell}, false)
	if errors.Is(err, variablestore.ErrNotFound) {
		return &variablestorev1.SetEnvSourceValueResponse{Created: creating}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.SetEnvSourceValueResponse{Metadata: metadataProto(scope, value.Metadata), Created: creating}, nil
}

func refuseOverwrite(envSource, key, url string) error {
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
		"%s already has %s, and write \"missing\" never overwrites a value there: change it in %s%s, or set write to \"values\" to let ocel update it",
		envSource, key, envSource, url))
}

func copiedFrom(ctx context.Context, store variablestore.Store, scope variablestore.Scope, at variablestore.Cell, envSource string) (*variablestore.Value, error) {
	stored, err := store.Get(ctx, scope, variablestore.Coordinate{Cell: at}, false)
	if errors.Is(err, variablestore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	if stored.Target != nil || stored.Provenance.EnvSource != envSource {
		return nil, nil
	}
	return &stored, nil
}

func folderName(folder string) string {
	if folder == "" {
		return "the root"
	}
	return folder
}

func syncCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel env sync --preview"
	}
	return "ocel env sync"
}

func envSourceStatus(ctx context.Context, store variablestore.Store, scope variablestore.Scope, registration envsource.Registration) (*variablestorev1.EnvSourceStatus, error) {
	status, err := envsource.StatusOf(ctx, store, scope.Tier, registration)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	out := &variablestorev1.EnvSourceStatus{
		EnvSource:     registration.Descriptor.ID(),
		Scheduled:     registration.Descriptor.IsScheduled(),
		CanCreate:     registration.Descriptor.CanCreate(),
		CanUpdate:     registration.Descriptor.CanUpdate(),
		LastAttemptAt: unixSeconds(status.LastAttemptAt),
		LastSuccessAt: unixSeconds(status.LastSuccessAt),
		LastError:     status.LastError,
	}
	for _, folder := range slices.Sorted(maps.Keys(status.URLs)) {
		out.Links = append(out.Links, &variablestorev1.FolderLink{Folder: folder, Url: status.URLs[folder]})
	}
	for _, credential := range registration.Credentials() {
		out.Credentials = append(out.Credentials, credential.Key)
	}
	return out, nil
}

func refuseEnvSourceOwned(ctx context.Context, store variablestore.Store, scope variablestore.Scope, at variablestore.Coordinate, removing bool) error {
	if at.Environment != "" {
		return nil
	}
	registration, registered, err := envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project)
	if err != nil {
		return provider.RefusalError(err)
	}
	if !registered || slices.Contains(registration.Credentials(), at.Cell) {
		return nil
	}
	owner := registration.Descriptor.ID()
	if removing {
		stored, err := store.Get(ctx, scope, at, false)
		if errors.Is(err, variablestore.ErrNotFound) {
			return nil
		}
		if err != nil {
			return valuesError(err)
		}
		if stored.Provenance.EnvSource != owner {
			return nil
		}
	}
	status, err := envsource.StatusOf(ctx, store, scope.Tier, registration)
	if err != nil {
		return provider.RefusalError(err)
	}
	copiedOn := "the next deploy"
	if registration.Descriptor.IsScheduled() {
		copiedOn = "its next sync, within a minute"
	}
	perEnvironment := ""
	if scope.Tier == environment.TierPreview {
		perEnvironment = " A value for one preview environment is still yours to set with --environment <name>."
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s is read from %s, which owns every value %s sets for all of %s: change it there%s, and ocel copies it on %s.%s",
		at, owner, scope.Project, scope.Tier, parenthesized(status.URLs[at.Folder]), copiedOn, perEnvironment))
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
		rpcErr := connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.Join(messages, "\n")))
		for _, credential := range refused {
			if detail, err := connect.NewErrorDetail(&variablestorev1.CredentialRefusal{Variable: credential.Variable, Unset: credential.Unset, Reason: credential.Reason}); err == nil {
				rpcErr.AddDetail(detail)
			}
		}
		return rpcErr
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

func cellProto(at variablestore.Cell) *variablestorev1.Cell {
	return &variablestorev1.Cell{Folder: at.Folder, Key: at.Key}
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

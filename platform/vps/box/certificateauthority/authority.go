package certificateauthority

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/box/usercertificate"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var certifiedPurposes = []string{"deploy", "test"}

type Config struct {
	Principal string
	Sign      func(ctx context.Context, request boxapi.CertificateRequest) (string, error)
	HostKeys  func() ([]string, error)
	Tiers     []environment.Tier
	Now       func() time.Time
}

type Authority struct {
	principal string
	sign      func(ctx context.Context, request boxapi.CertificateRequest) (string, error)
	hostKeys  func() ([]string, error)
	tiers     []environment.Tier
	now       func() time.Time
}

func New(config Config) (*Authority, error) {
	if config.Principal == "" {
		return nil, errors.New("certificate authority: no principal to sign certificates for")
	}
	if config.Sign == nil {
		return nil, errors.New("certificate authority: nothing signs its certificates")
	}
	authority := &Authority{
		principal: config.Principal,
		sign:      config.Sign,
		hostKeys:  config.HostKeys,
		tiers:     slices.Clone(config.Tiers),
		now:       config.Now,
	}
	if authority.hostKeys == nil {
		authority.hostKeys = func() ([]string, error) { return nil, nil }
	}
	if authority.now == nil {
		authority.now = time.Now
	}
	return authority, nil
}

func (a *Authority) Mount() endpoint.Mount {
	path, handler := endpointv1connect.NewSshCertificateServiceHandler(a, connect.WithInterceptors(validate.NewInterceptor()))
	return endpoint.Mount{Path: path, Handler: handler}
}

func (a *Authority) IssueUserCertificate(ctx context.Context, request *endpointv1.IssueUserCertificateRequest) (*endpointv1.IssueUserCertificateResponse, error) {
	credential, err := a.admit(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now()
	after := now.Add(-usercertificate.Backdate)
	before := after.Add(usercertificate.Lifetime)
	if !credential.Expires.IsZero() && credential.Expires.Before(before) {
		before = credential.Expires
	}
	if !before.After(now) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the credential has expired: ask the console for a new one"))
	}
	certificate, err := a.sign(ctx, boxapi.CertificateRequest{
		PublicKey:   request.GetPublicKey(),
		Principal:   a.principal,
		KeyID:       keyID(credential),
		ValidAfter:  after.Unix(),
		ValidBefore: before.Unix(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign the certificate: %w", err))
	}
	hostKeys, err := a.hostKeys()
	if err != nil {
		return nil, fmt.Errorf("read this box's host keys: %w", err)
	}
	return &endpointv1.IssueUserCertificateResponse{
		Certificate: certificate,
		Principal:   a.principal,
		ExpiresAt:   timestamppb.New(time.Unix(before.Unix(), 0)),
		HostKeys:    hostKeys,
	}, nil
}

func (a *Authority) admit(ctx context.Context) (endpoint.TargetCredential, error) {
	caller, _ := endpoint.CallerOf(ctx)
	credential, ok := caller.Credential()
	if !ok {
		return endpoint.TargetCredential{}, connect.NewError(connect.CodePermissionDenied, errors.New("only a credential the console issued for this box's target gets a certificate"))
	}
	if !slices.Contains(certifiedPurposes, credential.Purpose) {
		return endpoint.TargetCredential{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("the credential is for %q, and a certificate is issued only to deploy or to test the connection", credential.Purpose))
	}
	if !slices.Contains(a.tiers, credential.Tier) {
		return endpoint.TargetCredential{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this box trusts the console for no %s deploys: run `%s` logged in to the console", credential.Tier, provider.BootstrapCommand(credential.Tier)))
	}
	return credential, nil
}

var unsafeInSessionName = regexp.MustCompile(`[^\w+=,.@-]`)

func keyID(credential endpoint.TargetCredential) string {
	return strings.Join([]string{sessionName(credential.TokenID), credential.ProjectID, string(credential.Tier)}, " ")
}

func sessionName(tokenID string) string {
	short := tokenID
	if len(short) > 12 {
		short = short[:12]
	}
	return unsafeInSessionName.ReplaceAllString("ocel-"+short, "-")
}

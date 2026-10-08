package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type refusingIdentity struct{ err error }

func (r refusingIdentity) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return nil, r.err
}

func TestWhoamiSaysWhySTSRefused(t *testing.T) {
	t.Parallel()

	creds := Credentials{STS: refusingIdentity{err: errors.New("ExpiredToken: the security token included in the request is expired")}}
	_, err := creds.Whoami(context.Background())
	if err == nil {
		t.Fatal("Whoami() = nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "ExpiredToken") || !strings.Contains(err.Error(), credentialHint) {
		t.Errorf("err = %q, want STS's own reason beside the hint, so an expired session reads differently from no credentials at all", err)
	}
}

func callerIdentityFailure(cause error) error {
	return &smithy.OperationError{
		ServiceID:     "STS",
		OperationName: "GetCallerIdentity",
		Err:           fmt.Errorf("failed to sign request: failed to retrieve credentials: %w", cause),
	}
}

func TestWhoamiHintsAtTheFixForTheCauseSTSNamed(t *testing.T) {
	t.Parallel()

	assumeRoleDenied := &smithy.OperationError{
		ServiceID:     "STS",
		OperationName: "AssumeRole",
		Err: &smithy.GenericAPIError{
			Code:    "AccessDenied",
			Message: "User: arn:aws:iam::123456789012:user/dev is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::123456789012:role/deploy",
		},
	}
	cases := []struct {
		name     string
		err      error
		code     refusal.Code
		hint     string
		sdkWords string
	}{
		{
			name:     "an assume-role denial names the role's permissions, not the configure-credentials hint",
			err:      callerIdentityFailure(assumeRoleDenied),
			code:     refusal.CodeDenied,
			hint:     assumeRoleHint,
			sdkWords: "is not authorized to perform: sts:AssumeRole",
		},
		{
			name:     "an expired SSO session asks for a new login",
			err:      callerIdentityFailure(&ssocreds.InvalidTokenError{Err: errors.New("the SSO session has expired or is invalid")}),
			code:     refusal.CodeDenied,
			hint:     expiredHint,
			sdkWords: "the SSO session has expired or is invalid",
		},
		{
			name:     "expired temporary credentials ask for a refresh",
			err:      &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity", Err: &smithy.GenericAPIError{Code: "ExpiredToken", Message: "The security token included in the request is expired"}},
			code:     refusal.CodeDenied,
			hint:     expiredHint,
			sdkWords: "The security token included in the request is expired",
		},
		{
			name:     "a missing region is a misconfiguration naming where the region is set",
			err:      &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity", Err: errors.New("failed to resolve service endpoint, endpoint rule error, Invalid Configuration: Missing Region")},
			code:     refusal.CodeInvalid,
			hint:     regionHint,
			sdkWords: "Missing Region",
		},
		{
			name:     "an unreachable endpoint is a transport failure",
			err:      &smithy.OperationError{ServiceID: "STS", OperationName: "GetCallerIdentity", Err: &smithyhttp.RequestSendError{Err: errors.New("dial tcp: lookup sts.eu-west-1.amazonaws.com: no such host")}},
			code:     refusal.CodeNotReady,
			hint:     unreachableHint,
			sdkWords: "no such host",
		},
		{
			name:     "credentials that resolve to nothing keep the configure-credentials hint",
			err:      callerIdentityFailure(errors.New("failed to refresh cached credentials, no EC2 IMDS role found")),
			code:     refusal.CodeDenied,
			hint:     credentialHint,
			sdkWords: "no EC2 IMDS role found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Credentials{STS: refusingIdentity{err: tc.err}}.Whoami(context.Background())
			var refused refusal.Refusal
			if !errors.As(err, &refused) {
				t.Fatalf("Whoami() = %v, want a refusal", err)
			}
			if refused.Code != tc.code {
				t.Errorf("refusal code = %q, want %q", refused.Code, tc.code)
			}
			if !strings.HasPrefix(refused.Message, tc.hint+": ") || !strings.Contains(refused.Message, tc.sdkWords) {
				t.Errorf("refusal = %q, want the hint %q followed by what the SDK said (%q)", refused.Message, tc.hint, tc.sdkWords)
			}
		})
	}
}

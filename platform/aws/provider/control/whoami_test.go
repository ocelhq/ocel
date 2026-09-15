package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sts"
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

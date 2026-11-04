package deployreport

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestTheTargetIsTheFingerprintTheProviderNames(t *testing.T) {
	target, err := targetFrom(&contractv1.DescribeConnectorTargetResponse{TargetFingerprint: "vps/SHA256:AAAA/ocel"}, nil)
	if err != nil || target != "vps/SHA256:AAAA/ocel" {
		t.Errorf("target = %q, %v", target, err)
	}
}

func TestAProviderWithNoConnectorTargetsNamesNoTargetAndNoProblem(t *testing.T) {
	target, err := targetFrom(nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider puts no connector on its targets")))
	if err != nil || target != "" {
		t.Errorf("target = %q, %v, want none and no error", target, err)
	}
}

func TestATargetTheProviderCouldNotReadIsReportedWithItsCause(t *testing.T) {
	_, err := targetFrom(nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ocel-deploy over ssh: sudo: a password is required")))
	if err == nil || !strings.Contains(err.Error(), "a password is required") {
		t.Errorf("err = %v, want the cause named", err)
	}
}

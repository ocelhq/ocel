package component

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type fakeQuotas struct {
	applied  *float64
	fallback float64
	unlisted bool
	asked    []string
}

func (f *fakeQuotas) GetServiceQuota(_ context.Context, in *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	f.asked = append(f.asked, "applied "+aws.ToString(in.ServiceCode)+"/"+aws.ToString(in.QuotaCode))
	if f.applied == nil {
		return nil, &sqtypes.NoSuchResourceException{Message: aws.String("never changed")}
	}
	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: f.applied}}, nil
}

func (f *fakeQuotas) GetAWSDefaultServiceQuota(_ context.Context, in *servicequotas.GetAWSDefaultServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetAWSDefaultServiceQuotaOutput, error) {
	f.asked = append(f.asked, "default "+aws.ToString(in.ServiceCode)+"/"+aws.ToString(in.QuotaCode))
	if f.unlisted {
		return nil, &sqtypes.NoSuchResourceException{Message: aws.String("no such quota")}
	}
	return &servicequotas.GetAWSDefaultServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(f.fallback)}}, nil
}

func TestAFullQuotaIsRefusedNamingItsCodeAndHowToRaiseIt(t *testing.T) {
	t.Parallel()

	quotas := &fakeQuotas{applied: aws.Float64(5)}
	err := RefuseFullQuota(context.Background(), quotas, VPCQuota, "us-east-1", 5, 1, "the production tier's network")
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("RefuseFullQuota() = %v, want a refusal", err)
	}
	for _, named := range []string{"L-F678F1CE", "VPCs per Region", "5 of its 5", "request-service-quota-increase", "--service-code vpc", "--desired-value 6"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("refusal %q does not name %q", refused.Message, named)
		}
	}
	if err := RefuseFullQuota(context.Background(), quotas, VPCQuota, "us-east-1", 4, 1, "the production tier's network"); err != nil {
		t.Errorf("RefuseFullQuota() = %v, want room for one more accepted", err)
	}
}

func TestAQuotaNeverChangedIsReadAtItsAWSDefault(t *testing.T) {
	t.Parallel()

	quotas := &fakeQuotas{fallback: 5}
	if err := RefuseFullQuota(context.Background(), quotas, VPCQuota, "us-east-1", 3, 2, "the networks"); err != nil {
		t.Fatalf("RefuseFullQuota() = %v, want 3 + 2 within a default of 5", err)
	}
	if len(quotas.asked) != 2 || quotas.asked[1] != "default vpc/L-F678F1CE" {
		t.Errorf("asked %v, want the applied value and then the AWS default", quotas.asked)
	}
	if err := RefuseFullQuota(context.Background(), quotas, VPCQuota, "us-east-1", 4, 2, "the networks"); err == nil {
		t.Error("RefuseFullQuota() = nil, want 4 + 2 refused past a default of 5")
	}
}

func TestAQuotaServiceQuotasHoldsNoValueForIsLeftToAWSToEnforce(t *testing.T) {
	t.Parallel()

	quotas := &fakeQuotas{unlisted: true}
	if err := RefuseFullQuota(context.Background(), quotas, VPCQuota, "us-east-1", 40, 1, "the production tier's network"); err != nil {
		t.Errorf("RefuseFullQuota() = %v, want nothing refused: Service Quotas has neither an applied nor a default value for it here, so AWS refuses the create itself if the account is full", err)
	}
}

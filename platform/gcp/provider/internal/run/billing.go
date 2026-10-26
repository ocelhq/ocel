package run

import "github.com/ocelhq/ocel/pkg/provider"

type Billing string

const (
	BillingRequest  Billing = "request"
	BillingInstance Billing = "instance"
)

func ResolveBilling(chosen Billing, compute provider.Compute) Billing {
	if chosen != "" {
		return chosen
	}
	if compute == provider.ComputeServerless {
		return BillingRequest
	}
	return BillingInstance
}

func DefaultBilling(compute provider.Compute, servesNext bool) Billing {
	if servesNext {
		return BillingInstance
	}
	return ResolveBilling("", compute)
}

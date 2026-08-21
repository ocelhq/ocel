package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func (s *Server) substrateStatus(deployed bootstrap.Deployed, tier environmentv1.Tier, required []string) *contractv1.SubstrateStatus {
	status := &contractv1.SubstrateStatus{
		Tier:           tier,
		Present:        deployed.Present,
		Schema:         uint32(deployed.Schema),
		RequiredSchema: bootstrap.RequiredSchema,
		AutoHeal:       deployed.AutoHeal,
		Writer:         s.writer.String(),
	}
	for _, stack := range deployed.Stacks {
		status.Stacks = append(status.Stacks, &contractv1.SubstrateStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        uint32(stack.Schema),
			DigestCurrent: stack.Current(),
			WrittenBy:     stack.WrittenBy,
			Required:      stack.Feature == "" || slices.Contains(required, stack.Feature),
		})
		if stack.Feature == "" {
			status.Downgrade = bootstrap.Writer(stack.WrittenBy).Newer(s.writer)
		}
	}
	return status
}

func driftReport(deployed bootstrap.Deployed, required []string, bootstrapCmd string) string {
	stale := deployed.Stale(required)
	if len(stale) == 0 {
		return ""
	}
	names := make([]string, 0, len(stale))
	for _, stack := range stale {
		names = append(names, stack.Name)
	}
	return fmt.Sprintf(
		"this AWS account's Ocel bootstrap is the shape this build needs but its content is behind: %s. Re-run `%s` to refresh it",
		strings.Join(names, ", "), bootstrapCmd,
	)
}

func schemaAheadRefusal(deployed int, preview bool) error {
	return fmt.Errorf(
		"this AWS account's Ocel bootstrap is newer than this provider understands: the account is at schema %d, this provider supports up to schema %d.\nUpgrade the Ocel CLI, or run `%s` and bootstrap it afresh — there is no way to write an older shape over a newer one",
		deployed, bootstrap.RequiredSchema, bootstrapDestroyCommand(preview),
	)
}

func healableStacks(deployed bootstrap.Deployed, required []string) []string {
	var out []string
	for _, stack := range deployed.Stale(required) {
		if stack.Feature != "" {
			out = append(out, stack.Name)
		}
	}
	return out
}

func substrateWriter(deployed bootstrap.Deployed) bootstrap.Writer {
	for _, stack := range deployed.Stacks {
		if stack.Feature == "" {
			return bootstrap.Writer(stack.WrittenBy)
		}
	}
	return ""
}

func (s *Server) healSubstrate(ctx context.Context, awscfg aws.Config, deployed bootstrap.Deployed, required []string, preview bool, logf func(string)) (bool, error) {
	if !deployed.AutoHeal || len(healableStacks(deployed, required)) == 0 {
		return false, nil
	}
	if !s.writer.Release() {
		logf(fmt.Sprintf("this provider is a development build (%s), so it leaves the account's stale bootstrap stacks as they are", s.writer))
		return false, nil
	}
	if written := substrateWriter(deployed); !written.Release() {
		logf(fmt.Sprintf("this account's bootstrap was written by a development build (%s), so it is refreshed only by the run that writes it next", written))
		return false, nil
	}
	apis := bootstrap.APIs{
		CFN:   cloudformation.NewFromConfig(awscfg),
		Store: s3.NewFromConfig(awscfg),
	}
	healed, err := healRunner(preview)(ctx, apis, bootstrap.HealRequest{Features: required, Writer: s.writer}, logf)
	if errors.Is(err, bootstrap.ErrHealNotPermitted) {
		logf("refreshing this account's stale bootstrap stacks needs bootstrap-tier credentials and this run holds deploy-tier ones, so the substrate is left as it stands")
		return healed, nil
	}
	return healed, err
}

type healRun func(ctx context.Context, apis bootstrap.APIs, req bootstrap.HealRequest, log func(string)) (bool, error)

func healRunner(preview bool) healRun {
	if preview {
		return bootstrap.HealPreview
	}
	return bootstrap.Heal
}

func bootstrapDestroyCommand(preview bool) string {
	if preview {
		return "ocel bootstrap --destroy --preview"
	}
	return "ocel bootstrap --destroy"
}

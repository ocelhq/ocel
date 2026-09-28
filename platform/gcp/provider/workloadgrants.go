package gcp

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	workloadRecordsRole = "roles/datastore.viewer"
	workloadOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"

	reasonUnread = "it exists, and it may not read this project's records or open one under the tier key, so a container's runtime would boot with none of its secrets"
)

var workloadKeyRoles = []string{workloadOpeningRole}

func workloadMember(c *clients, tier environment.Tier) string {
	return "serviceAccount:" + c.WorkloadAccountEmail(tier)
}

func (b bootstrap) grantReads(ctx context.Context, tier environment.Tier) error {
	member := workloadMember(b.clients, tier)
	condition := databaseCondition(b.clients.project, b.clients.Namespace())
	if err := b.clients.bindProjectRole(ctx, member, workloadRecordsRole, condition, true); err != nil {
		return fmt.Errorf("let %s read the %s database's records: %w", member, ports.Database(b.clients.Namespace()), err)
	}
	if _, err := b.clients.bindKeyRoles(ctx, tier, member, workloadKeyRoles, workloadKeyRoles); err != nil {
		return fmt.Errorf("let %s open values under the %s key: %w", member, tier, err)
	}
	return nil
}

func (b bootstrap) forgetReads(ctx context.Context, tier environment.Tier) error {
	member := workloadMember(b.clients, tier)
	condition := databaseCondition(b.clients.project, b.clients.Namespace())
	return everyStep(
		func() error { return b.clients.bindProjectRole(ctx, member, workloadRecordsRole, condition, false) },
		func() error {
			_, err := b.clients.bindKeyRoles(ctx, tier, member, workloadKeyRoles, nil)
			return err
		},
	)
}

func (b bootstrap) readsGranted(ctx context.Context, tier environment.Tier) (bool, error) {
	member := workloadMember(b.clients, tier)
	records, err := b.clients.projectRoleGranted(ctx, member, workloadRecordsRole, databaseCondition(b.clients.project, b.clients.Namespace()))
	if err != nil || !records {
		return false, err
	}
	return b.clients.keyRolesGranted(ctx, tier, member, workloadKeyRoles)
}

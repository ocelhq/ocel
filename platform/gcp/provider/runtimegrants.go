package gcp

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	runtimeRecordsRole = "roles/datastore.viewer"
	runtimeOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"

	reasonUnread = "it stands, and it may not read this project's records or open one under the class key, so a container's runtime would boot with none of its secrets"
)

var runtimeKeyRoles = []string{runtimeOpeningRole}

func runtimeMember(c *clients, class providerkit.Class) string {
	return "serviceAccount:" + c.RuntimeAccountEmail(class)
}

func (b bootstrapper) grantReads(ctx context.Context, class providerkit.Class) error {
	member := runtimeMember(b.clients, class)
	condition := databaseCondition(b.clients.project, b.clients.Namespace())
	if err := b.clients.bindProjectRole(ctx, member, runtimeRecordsRole, condition, true); err != nil {
		return fmt.Errorf("let %s read the %s database's records: %w", member, ports.Database(b.clients.Namespace()), err)
	}
	if _, err := b.clients.bindKeyRoles(ctx, class, member, runtimeKeyRoles, runtimeKeyRoles); err != nil {
		return fmt.Errorf("let %s open values under the %s key: %w", member, class, err)
	}
	return nil
}

func (b bootstrapper) forgetReads(ctx context.Context, class providerkit.Class) error {
	member := runtimeMember(b.clients, class)
	condition := databaseCondition(b.clients.project, b.clients.Namespace())
	return everyStep(
		func() error { return b.clients.bindProjectRole(ctx, member, runtimeRecordsRole, condition, false) },
		func() error {
			_, err := b.clients.bindKeyRoles(ctx, class, member, runtimeKeyRoles, nil)
			return err
		},
	)
}

func (b bootstrapper) readsHeld(ctx context.Context, class providerkit.Class) (bool, error) {
	member := runtimeMember(b.clients, class)
	records, err := b.clients.projectRoleHeld(ctx, member, runtimeRecordsRole, databaseCondition(b.clients.project, b.clients.Namespace()))
	if err != nil || !records {
		return false, err
	}
	return b.clients.keyRolesHeld(ctx, class, member, runtimeKeyRoles)
}

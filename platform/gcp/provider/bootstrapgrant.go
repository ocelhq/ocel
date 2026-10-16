package gcp

import (
	"context"
	"fmt"
)

var projectGrantees = map[string]string{
	networkUserRole: runAgentDomain,
}

func (b bootstrap) readProjectGrantee(ctx context.Context, role string) (string, error) {
	domain, known := projectGrantees[role]
	if !known {
		return "", fmt.Errorf("no service agent is granted %s on the project", role)
	}
	return b.clients.ReadServiceAgent(ctx, domain)
}

func (b bootstrap) bindProjectGrant(ctx context.Context, role string, granting bool) error {
	agent, err := b.readProjectGrantee(ctx, role)
	if err != nil {
		return err
	}
	return b.clients.bindProjectRole(ctx, agent, role, nil, granting)
}

func (b bootstrap) projectGrantBound(ctx context.Context, role string) (bool, error) {
	agent, err := b.readProjectGrantee(ctx, role)
	if err != nil {
		return false, err
	}
	return b.clients.projectRoleGranted(ctx, agent, role, nil)
}

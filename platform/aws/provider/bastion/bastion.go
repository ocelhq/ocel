package bastion

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	Prefix                 = "ocel-bastion"
	PortForwardingDocument = "AWS-StartPortForwardingSessionToRemoteHost"

	containerName       = "bastion"
	image               = "public.ecr.aws/docker/library/busybox:1.37"
	taskCPU             = "256"
	taskMemoryMiB       = "512"
	taskLifetimeSeconds = "7200"
	ecsTasksPrincipal   = "ecs-tasks.amazonaws.com"
	sessionPolicyName   = "session-channels"
	httpsPort           = 443
	anywhere            = "0.0.0.0/0"

	managedByTagKey   = "ocel:managed-by"
	managedByTagValue = "ocel"

	defaultPollInterval = 2 * time.Second
	pollJitter          = 0.5
	pollCeiling         = 10 * time.Second
	agentReadyTimeout   = 5 * time.Minute
	removalTimeout      = 3 * time.Minute
	stopTimeout         = 30 * time.Second
)

func NameFor(tier environment.Tier) string {
	return naming.Join(naming.WordSeparator, Prefix, string(tier))
}

func tags(tier environment.Tier) map[string]string {
	return map[string]string{managedByTagKey: managedByTagValue, naming.EnvTierTagKey: string(tier)}
}

type Clients struct {
	ECS ECSAPI
	IAM IAMAPI
	EC2 EC2API

	PollInterval time.Duration
}

func (c Clients) pause(ctx context.Context, attempt int) error {
	every := c.PollInterval
	if every == 0 {
		every = defaultPollInterval
	}
	delay := min(every*time.Duration(1+attempt/3), pollCeiling)
	delay += time.Duration(float64(delay) * pollJitter * (2*rand.Float64() - 1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Spec struct {
	Tier     environment.Tier
	Boundary string
	Ports    []int
}

type Bastion struct {
	Tier           environment.Tier
	Cluster        string
	TaskDefinition string
	SecurityGroup  string
	Subnets        []string
	Ports          []int
}

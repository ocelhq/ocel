package bastion

import (
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

	pollJitter        = 0.5
	pollCeiling       = 10 * time.Second
	agentReadyTimeout = 5 * time.Minute
	removalTimeout    = 3 * time.Minute
	stopTimeout       = 30 * time.Second
	maxRoleAttempts   = 12
)

func NameFor(tier environment.Tier) string {
	return naming.Join(naming.WordSeparator, Prefix, string(tier))
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

package bastion

import (
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	Part    = "bastion"
	Summary = "the task a port forward runs inside the tier's network"

	word                   = "bastion"
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

	pollJitter        = 0.5
	pollCeiling       = 10 * time.Second
	agentReadyTimeout = 5 * time.Minute
	removalTimeout    = 3 * time.Minute
	stopTimeout       = 30 * time.Second
	maxRoleAttempts   = 12
)

func NameFor(namespace string, tier environment.Tier) string {
	return naming.OcelName(namespace, tier, word)
}

type Spec struct {
	Namespace string
	Tier      environment.Tier
	Boundary  string
	Ports     []int
	Clients   string
}

type Bastion struct {
	Tier           environment.Tier
	Cluster        string
	TaskDefinition string
	SecurityGroup  string
	Clients        string
	Subnets        []string
	Ports          []int
}

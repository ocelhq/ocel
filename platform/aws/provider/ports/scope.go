package ports

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	Vendor = "aws"

	ContainersSlug = "ocel-containers"

	PublicListenerPort = 443

	reservedBalancerPrefix = "internal-"

	containerFrontSegment = "front"
	containersWord        = "containers"
	publicWord            = "public"
	livenessWord          = "liveness"
	targetGroupWord       = "tg"

	RuntimeLayerToPublish = "ocel:runtime-layer-to-publish"
)

type ContainerFront struct {
	VPCOrigin string `json:"vpcOrigin"`
	Host      string `json:"host"`
}

func ContainerClusterName(namespace string, tier environment.Tier) string {
	return naming.OcelName(namespace, tier, containersWord)
}

func ContainerLogGroupName(namespace string, tier environment.Tier) string {
	return "/ecs/" + ContainerClusterName(namespace, tier)
}

func PublicBalancerName(namespace string, tier environment.Tier) string {
	return naming.OcelName(namespace, tier, publicWord)
}

func PublicTargetGroupName(namespace string, tier environment.Tier) string {
	return naming.Join(naming.WordSeparator, PublicBalancerName(namespace, tier), targetGroupWord)
}

func PublicLivenessName(namespace string, tier environment.Tier) string {
	return naming.Join(naming.WordSeparator, PublicBalancerName(namespace, tier), livenessWord)
}

func RefuseReservedBalancerPrefix(namespace string) error {
	if strings.HasPrefix(naming.NamespaceField(namespace)+naming.WordSeparator, reservedBalancerPrefix) {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s: namespace %q would start every load balancer name with %q, a prefix Elastic Load Balancing reserves; choose another namespace",
			provider.NamespaceEnvVar, namespace, reservedBalancerPrefix)
	}
	return nil
}

func ContainerFrontKey(tier environment.Tier) records.Key {
	return stackrecords.StacksPartition(tier, ContainersSlug).Key(containerFrontSegment)
}

func ReadContainerFront(ctx context.Context, store records.Store, tier environment.Tier) (ContainerFront, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, store, ContainerFrontKey(tier))
	if err != nil {
		return ContainerFront{}, false, err
	}
	if len(entry.Value) == 0 {
		return ContainerFront{}, false, nil
	}
	var front ContainerFront
	if err := json.Unmarshal(entry.Value, &front); err != nil {
		return ContainerFront{}, false, fmt.Errorf("read the container front %s holds: %w", entry.Key, err)
	}
	if front.VPCOrigin == "" || front.Host == "" {
		return ContainerFront{}, false, fmt.Errorf("the container front %s holds names no VPC origin or host", entry.Key)
	}
	return front, true, nil
}

func WriteContainerFront(ctx context.Context, store records.Store, tier environment.Tier, front ContainerFront) error {
	current, err := records.ReadOrEmpty(ctx, store, ContainerFrontKey(tier))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(front)
	if err != nil {
		return fmt.Errorf("encode the container front: %w", err)
	}
	if string(current.Value) == string(encoded) {
		return nil
	}
	current.Value = encoded
	if _, err := store.Write(ctx, current); err != nil {
		return fmt.Errorf("record the container front the %s tier answers behind: %w", tier, err)
	}
	return nil
}

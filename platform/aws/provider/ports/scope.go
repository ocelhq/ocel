package ports

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	AppScope = "ocel-app"

	ContainersSlug = "ocel-containers"

	containerFrontSegment = "front"
)

type ContainerFront struct {
	VPCOrigin string `json:"vpcOrigin"`
	Host      string `json:"host"`
}

func ContainerFrontKey(tier environment.Tier) keyvalue.Key {
	return stackrecords.StacksPartition(tier, ContainersSlug).Key(containerFrontSegment)
}

func ReadContainerFront(ctx context.Context, store keyvalue.Store, tier environment.Tier) (ContainerFront, bool, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, store, ContainerFrontKey(tier))
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

func WriteContainerFront(ctx context.Context, store keyvalue.Store, tier environment.Tier, front ContainerFront) error {
	current, err := keyvalue.ReadOrEmpty(ctx, store, ContainerFrontKey(tier))
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

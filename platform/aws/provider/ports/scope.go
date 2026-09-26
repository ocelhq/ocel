package ports

import (
	"context"
	"encoding/json"
	"fmt"

	kit "github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	AppScope = "ocel-app"

	ContainersSlug = "ocel-containers"

	containerFrontRecord = "front"
)

type ContainerFront struct {
	VPCOrigin string `json:"vpcOrigin"`
	Host      string `json:"host"`
}

func ContainerFrontRecord(class edge.Class) records.Name {
	return append(kit.StacksRecord(class, ContainersSlug), containerFrontRecord)
}

func ReadContainerFront(ctx context.Context, store records.Store, class edge.Class) (ContainerFront, bool, error) {
	held, err := records.ReadOrEmpty(ctx, store, ContainerFrontRecord(class))
	if err != nil {
		return ContainerFront{}, false, err
	}
	if len(held.Bytes) == 0 {
		return ContainerFront{}, false, nil
	}
	var front ContainerFront
	if err := json.Unmarshal(held.Bytes, &front); err != nil {
		return ContainerFront{}, false, fmt.Errorf("read the container front %s records: %w", held.Name, err)
	}
	if front.VPCOrigin == "" || front.Host == "" {
		return ContainerFront{}, false, fmt.Errorf("the container front %s records names no VPC origin or host", held.Name)
	}
	return front, true, nil
}

func WriteContainerFront(ctx context.Context, store records.Store, class edge.Class, front ContainerFront) error {
	held, err := records.ReadOrEmpty(ctx, store, ContainerFrontRecord(class))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(front)
	if err != nil {
		return fmt.Errorf("encode the container front: %w", err)
	}
	if string(held.Bytes) == string(encoded) {
		return nil
	}
	held.Bytes = encoded
	if _, err := store.Write(ctx, held); err != nil {
		return fmt.Errorf("record the container front the %s class answers behind: %w", class, err)
	}
	return nil
}

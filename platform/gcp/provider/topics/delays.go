package topics

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	PublishURL      = "https://pubsub.googleapis.com"
	taskPrefixBytes = 4
)

type Delays struct {
	Queue      string
	Account    string
	PublishURL string
}

func (d Delays) Ensure(ctx context.Context, clients *ports.Clients) error {
	client, err := clients.CloudTasks()
	if err != nil {
		return err
	}
	parent, _, _ := strings.Cut(d.Queue, "/queues/")
	err = googleretry.Done(ctx, func() error {
		_, err := client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: parent, Queue: &cloudtaskspb.Queue{Name: d.Queue}})
		return err
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create Cloud Tasks queue %s: %w", d.Queue, err)
	}
	return nil
}

func (d Delays) taskOf(msg runs.Message) string {
	token := cmp.Or(msg.Delivery, msg.ID)
	return d.Queue + "/tasks/" + hashedTaskPrefix(token) + "-" + token
}

func hashedTaskPrefix(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:taskPrefixBytes])
}

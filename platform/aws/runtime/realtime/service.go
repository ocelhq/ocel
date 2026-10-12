package realtime

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
)

func NewAppSyncService(records proxy.Records, cfg aws.Config) *proxy.Service {
	return proxy.NewService(records, NewAppSyncTransport(Config{
		Client:      http.DefaultClient,
		Credentials: cfg.Credentials,
		Retryer:     sdkconfig.WorkloadRetryer(),
		Region:      cfg.Region,
	}))
}

package console

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const cloudOfferTTL = 5 * time.Minute

type CloudOffer struct {
	APIURL      string `json:"api_url"`
	ProviderURL string `json:"provider_url,omitempty"`
	Audience    string `json:"audience"`
	Version     string `json:"version,omitempty"`
}

func (c *Client) consoleService(accessToken string) consolev1connect.ConsoleServiceClient {
	return consolev1connect.NewConsoleServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken), connect.WithProtoJSON())
}

func (c *Client) Describe(ctx context.Context) (*consolev1.DescribeResponse, error) {
	return c.consoleService("").Describe(ctx, &consolev1.DescribeRequest{})
}

func (c *Client) ReadCloudOffer(ctx context.Context) (*CloudOffer, error) {
	described, err := c.Describe(ctx)
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ask %s what it offers: %w", c.baseURL, err)
	}
	cloud := described.GetCloud()
	if cloud == nil {
		return nil, nil
	}
	return &CloudOffer{APIURL: cloud.GetApiUrl(), ProviderURL: cloud.GetProviderUrl(), Audience: cloud.GetAudience(), Version: cloud.GetVersion()}, nil
}

type CloudOffers struct {
	load func() (Credentials, error)
	save func(Credentials) (CredentialStore, error)
	now  func() time.Time

	mu        sync.Mutex
	described map[string]*CloudOffer
}

func NewCloudOffers(load func() (Credentials, error), save func(Credentials) (CredentialStore, error)) *CloudOffers {
	return &CloudOffers{load: load, save: save, now: time.Now, described: map[string]*CloudOffer{}}
}

func (o *CloudOffers) Resolve(ctx context.Context, creds Credentials) (*CloudOffer, error) {
	baseURL := BaseURL(creds.APIURL)
	stored := creds.Store != EnvironmentStore && strings.TrimRight(strings.TrimSpace(creds.APIURL), "/") == baseURL
	if stored && !creds.CloudFetchedAt.IsZero() && o.now().Sub(creds.CloudFetchedAt) < cloudOfferTTL {
		return creds.Cloud, nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if offer, described := o.described[baseURL]; described {
		return offer, nil
	}
	offer, err := New(baseURL).ReadCloudOffer(ctx)
	if err != nil {
		return nil, err
	}
	o.described[baseURL] = offer
	if stored {
		if err := o.saveOffer(creds, offer); err != nil {
			return nil, err
		}
	}
	return offer, nil
}

func (o *CloudOffers) saveOffer(read Credentials, offer *CloudOffer) error {
	current, err := o.load()
	if err != nil || current.AccessToken != read.AccessToken || current.APIURL != read.APIURL {
		return nil
	}
	current.Cloud = offer
	current.CloudFetchedAt = o.now()
	if _, err := o.save(current); err != nil {
		return fmt.Errorf("save what the console offers: %w", err)
	}
	return nil
}

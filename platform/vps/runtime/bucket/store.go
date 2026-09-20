package bucket

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Store names an S3-compatible endpoint and the credential that reaches it.
type Store struct {
	// Endpoint is the address the store answers on.
	Endpoint string
	// Region the signature is scoped to; stores that have none still sign against one.
	Region string
	// AccessKeyID is the credential's public half.
	AccessKeyID string
	// SecretAccessKey is the credential's secret half.
	SecretAccessKey string
	// PathStyle addresses buckets as a path segment rather than a subdomain.
	PathStyle bool
}

// Client builds the S3 client this store is reached with.
func (s Store) Client() *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:       s.Region,
		BaseEndpoint: aws.String(s.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			s.AccessKeyID, s.SecretAccessKey, ""),
	}, func(o *s3.Options) { o.UsePathStyle = s.PathStyle })
}

// Presigner builds the signer this store's urls are signed by.
func (s Store) Presigner() *s3.PresignClient {
	return s3.NewPresignClient(s.Client())
}

type HTTPPoster struct {
	Client *http.Client
	App    string
}

// Post delivers one callback body to the app's upload route.
func (p HTTPPoster) Post(ctx context.Context, url string, body []byte) error {
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if p.App != "" {
		req.Host = req.URL.Host
		req.URL.Scheme, req.URL.Host = "http", p.App
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("the app answered %s", resp.Status)
	}
	return nil
}

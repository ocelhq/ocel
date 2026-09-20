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

type Store struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

func (s Store) Client() *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:       s.Region,
		BaseEndpoint: aws.String(s.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			s.AccessKeyID, s.SecretAccessKey, ""),
	}, func(o *s3.Options) { o.UsePathStyle = s.PathStyle })
}

func (s Store) Presigner() *s3.PresignClient {
	return s3.NewPresignClient(s.Client())
}

type HTTPPoster struct {
	Client *http.Client
	App    string
}

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

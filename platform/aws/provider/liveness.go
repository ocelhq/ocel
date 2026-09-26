package aws

import (
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func emulatedFront(cfg aws.Config) *url.URL {
	if cfg.BaseEndpoint == nil {
		return nil
	}
	front, err := url.Parse(*cfg.BaseEndpoint)
	if err != nil || front.Host == "" {
		return nil
	}
	return front
}

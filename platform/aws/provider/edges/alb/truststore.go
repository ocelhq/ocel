package alb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	bundleWrites      = 5
	trustStoreReads   = 30
	trustStorePause   = 2 * time.Second
	verifyMode        = "verify"
	preconditionError = "PreconditionFailed"
)

func bundleKey(tier environment.Tier) string {
	return "ocel/trust/" + string(tier) + "/client-certificates.pem"
}

func splitCertificates(bundle string) []string {
	var certificates []string
	for _, block := range strings.SplitAfter(bundle, "-----END CERTIFICATE-----") {
		if trimmed := strings.TrimSpace(block); strings.HasPrefix(trimmed, "-----BEGIN CERTIFICATE-----") {
			certificates = append(certificates, trimmed)
		}
	}
	return certificates
}

func joinCertificates(certificates []string) string {
	return strings.Join(certificates, "\n") + "\n"
}

func (s *stack) trust(ctx context.Context, c Clients, answering front, certificates []string) error {
	trusted, err := s.writeBundle(ctx, c, certificates)
	if err != nil {
		return err
	}
	store, err := s.ensureTrustStore(ctx, c, trusted)
	if err != nil {
		return err
	}
	if mutual := answering.mutual; mutual != nil && aws.ToString(mutual.Mode) == verifyMode && aws.ToString(mutual.TrustStoreArn) == store {
		return nil
	}
	if _, err := c.Balancers.ModifyListener(ctx, &elbv2.ModifyListenerInput{
		ListenerArn: aws.String(answering.listener),
		MutualAuthentication: &elbv2types.MutualAuthenticationAttributes{
			Mode:          aws.String(verifyMode),
			TrustStoreArn: aws.String(store),
		},
	}); err != nil {
		return fmt.Errorf("require the client certificate the edge presents on %s: %w", answering.listener, err)
	}
	return nil
}

func (s *stack) writeBundle(ctx context.Context, c Clients, certificates []string) (bool, error) {
	key := bundleKey(s.state.Tier)
	for range bundleWrites {
		held, etag, err := readBundle(ctx, c, key)
		if err != nil {
			return false, err
		}
		merged := slices.Clone(held)
		for _, certificate := range certificates {
			if trimmed := strings.TrimSpace(certificate); !slices.Contains(merged, trimmed) {
				merged = append(merged, trimmed)
			}
		}
		if len(merged) == len(held) {
			return false, nil
		}
		put := &s3.PutObjectInput{
			Bucket: aws.String(c.Bucket),
			Key:    aws.String(key),
			Body:   bytes.NewReader([]byte(joinCertificates(merged))),
		}
		if etag == "" {
			put.IfNoneMatch = aws.String("*")
		} else {
			put.IfMatch = aws.String(etag)
		}
		_, err = c.Objects.PutObject(ctx, put)
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == preconditionError {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("record the client certificates the load balancer trusts: %w", err)
		}
		return true, nil
	}
	return false, fmt.Errorf("record the client certificates the load balancer trusts: another deploy changed them on each of %d attempts", bundleWrites)
}

func readBundle(ctx context.Context, c Clients, key string) ([]string, string, error) {
	read, err := c.Objects.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
	var absent *s3types.NoSuchKey
	if errors.As(err, &absent) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the client certificates the load balancer trusts: %w", err)
	}
	defer read.Body.Close()
	body, err := io.ReadAll(read.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read the client certificates the load balancer trusts: %w", err)
	}
	return splitCertificates(string(body)), aws.ToString(read.ETag), nil
}

func (s *stack) ensureTrustStore(ctx context.Context, c Clients, changed bool) (string, error) {
	name := awsports.PublicBalancerName(s.state.Tier)
	for attempt := range trustStoreReads {
		read, err := c.Balancers.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{Names: []string{name}})
		var absent *elbv2types.TrustStoreNotFoundException
		switch {
		case errors.As(err, &absent):
			if _, err := c.Balancers.CreateTrustStore(ctx, &elbv2.CreateTrustStoreInput{
				Name:                         aws.String(name),
				CaCertificatesBundleS3Bucket: aws.String(c.Bucket),
				CaCertificatesBundleS3Key:    aws.String(bundleKey(s.state.Tier)),
				Tags:                         []elbv2types.Tag{{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")}},
			}); err != nil {
				return "", fmt.Errorf("create the trust store %s: %w", name, err)
			}
			changed = false
		case err != nil:
			return "", fmt.Errorf("read the trust store %s: %w", name, err)
		case len(read.TrustStores) > 0 && read.TrustStores[0].Status == elbv2types.TrustStoreStatusActive:
			store := aws.ToString(read.TrustStores[0].TrustStoreArn)
			if !changed {
				return store, nil
			}
			if _, err := c.Balancers.ModifyTrustStore(ctx, &elbv2.ModifyTrustStoreInput{
				TrustStoreArn:                aws.String(store),
				CaCertificatesBundleS3Bucket: aws.String(c.Bucket),
				CaCertificatesBundleS3Key:    aws.String(bundleKey(s.state.Tier)),
			}); err != nil {
				return "", fmt.Errorf("trust the client certificates of %s: %w", name, err)
			}
			return store, nil
		}
		if err := pause(ctx, attempt); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("the trust store %s was not active after %d reads", name, trustStoreReads)
}

func pause(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	wait := trustStorePause/2 + rand.N(trustStorePause)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

package alb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	recordWrites      = 5
	trustStoreReads   = 30
	trustStorePause   = 2 * time.Second
	verifyMode        = "verify"
	offMode           = "off"
	preconditionError = "PreconditionFailed"
	bundleDigestTag   = "ocel:client-cas-sha256"
	caQuota           = "CA certificates per trust store (L-43FE5A42)"
	trustStoreQuota   = "Trust stores per account (L-A8F7060B)"
)

type trustRecord struct {
	Hostnames map[string][]string `json:"hostnames,omitempty"`
}

func (r trustRecord) listTrusted() []string {
	var trusted []string
	for _, authorities := range r.Hostnames {
		trusted = append(trusted, authorities...)
	}
	slices.Sort(trusted)
	return slices.Compact(trusted)
}

func formatRecordKey(tier environment.Tier) string {
	return "ocel/trust/" + string(tier) + "/client-cas.json"
}

func formatBundleKey(tier environment.Tier) string {
	return "ocel/trust/" + string(tier) + "/client-cas.pem"
}

func digestCertificates(certificates []string) string {
	sum := sha256.New()
	for _, certificate := range certificates {
		sum.Write([]byte(certificate))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func joinCertificates(certificates []string) string {
	return strings.Join(certificates, "\n") + "\n"
}

func refuseNonAuthorities(hostname string, authorities []string) error {
	for _, authority := range authorities {
		block, _ := pem.Decode([]byte(authority))
		if block == nil || block.Type != "CERTIFICATE" {
			return refusal.Refuse(refusal.CodeInvalid, "a client CA the edge names for %s is no PEM certificate", hostname)
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "a client CA the edge names for %s cannot be read: %v", hostname, err)
		}
		if !parsed.IsCA {
			return refusal.Refuse(refusal.CodeInvalid,
				"the edge presents a client certificate to %s that chains to %q, which is no CA, and the load balancer's trust store holds only CAs: delete that certificate from the zone (SSL/TLS > Origin Server > Authenticated Origin Pulls) so ocel uploads one of its own, and deploy again",
				hostname, parsed.Subject.String())
		}
	}
	return nil
}

func (s *stack) trustClientCAs(ctx context.Context, c Clients, hostname string, authorities []string) error {
	if err := refuseNonAuthorities(hostname, authorities); err != nil {
		return err
	}
	trusted := make([]string, 0, len(authorities))
	for _, authority := range authorities {
		trusted = append(trusted, strings.TrimSpace(authority))
	}
	slices.Sort(trusted)
	if err := s.updateTrustRecord(ctx, c, func(r trustRecord) trustRecord {
		r.Hostnames[hostname] = slices.Compact(trusted)
		return r
	}); err != nil {
		return err
	}
	return s.applyTrustRecord(ctx, c)
}

func (s *stack) untrustHostname(ctx context.Context, c Clients, hostname string) error {
	if err := s.updateTrustRecord(ctx, c, func(r trustRecord) trustRecord {
		delete(r.Hostnames, hostname)
		return r
	}); err != nil {
		return err
	}
	return s.applyTrustRecord(ctx, c)
}

func (s *stack) updateTrustRecord(ctx context.Context, c Clients, change func(trustRecord) trustRecord) error {
	key := formatRecordKey(s.state.Tier)
	for range recordWrites {
		held, etag, err := readTrustRecord(ctx, c, key)
		if err != nil {
			return err
		}
		prior := maps.Clone(held.Hostnames)
		if held.Hostnames == nil {
			held.Hostnames = map[string][]string{}
		}
		changed := change(held)
		if maps.EqualFunc(prior, changed.Hostnames, slices.Equal) {
			return nil
		}
		body, err := json.Marshal(changed)
		if err != nil {
			return err
		}
		put := &s3.PutObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key), Body: bytes.NewReader(body)}
		if etag == "" {
			put.IfNoneMatch = aws.String("*")
		} else {
			put.IfMatch = aws.String(etag)
		}
		_, err = c.Objects.PutObject(ctx, put)
		if isConditionFailed(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("record the client CAs the load balancer trusts: %w", err)
		}
		return nil
	}
	return fmt.Errorf("record the client CAs the load balancer trusts: another deploy changed them on each of %d attempts", recordWrites)
}

func readTrustRecord(ctx context.Context, c Clients, key string) (trustRecord, string, error) {
	read, err := c.Objects.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
	var absent *s3types.NoSuchKey
	if errors.As(err, &absent) {
		return trustRecord{}, "", nil
	}
	if err != nil {
		return trustRecord{}, "", fmt.Errorf("read the client CAs the load balancer trusts: %w", err)
	}
	defer read.Body.Close()
	body, err := io.ReadAll(read.Body)
	if err != nil {
		return trustRecord{}, "", fmt.Errorf("read the client CAs the load balancer trusts: %w", err)
	}
	var record trustRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return trustRecord{}, "", fmt.Errorf("read the client CAs the load balancer trusts: %w", err)
	}
	return record, aws.ToString(read.ETag), nil
}

func (s *stack) applyTrustRecord(ctx context.Context, c Clients) error {
	return s.newLease(c, formatTrustLeaseKey(s.state.Tier), "applying the client CAs the load balancer trusts").hold(ctx, func(ctx context.Context) error {
		record, etag, err := readTrustRecord(ctx, c, formatRecordKey(s.state.Tier))
		if err != nil {
			return err
		}
		listener, err := s.readPublicListener(ctx, c)
		var refused refusal.Refusal
		absent := errors.As(err, &refused) && refused.Code == refusal.CodeNotReady
		if err != nil && !absent {
			return err
		}
		trusted := record.listTrusted()
		if len(trusted) == 0 {
			return s.clearTrustStore(ctx, c, listener, etag)
		}
		if absent {
			return err
		}
		digest := digestCertificates(trusted)
		store, applied, err := s.readTrustStore(ctx, c)
		if err != nil {
			return err
		}
		if applied != digest {
			if store, err = s.writeTrustStore(ctx, c, store, trusted, digest); err != nil {
				return err
			}
		}
		return requireClientCertificates(ctx, c, listener, store)
	})
}

func requireClientCertificates(ctx context.Context, c Clients, listener publicListener, store string) error {
	if mutual := listener.mutual; mutual != nil && aws.ToString(mutual.Mode) == verifyMode && aws.ToString(mutual.TrustStoreArn) == store {
		return nil
	}
	if _, err := c.Balancers.ModifyListener(ctx, &elbv2.ModifyListenerInput{
		ListenerArn: aws.String(listener.arn),
		MutualAuthentication: &elbv2types.MutualAuthenticationAttributes{
			Mode:          aws.String(verifyMode),
			TrustStoreArn: aws.String(store),
		},
	}); err != nil {
		return fmt.Errorf("require the client certificate the edge presents on %s: %w", listener.arn, err)
	}
	return nil
}

func (s *stack) clearTrustStore(ctx context.Context, c Clients, listener publicListener, recordETag string) error {
	if mutual := listener.mutual; listener.arn != "" && mutual != nil && aws.ToString(mutual.Mode) != offMode {
		if _, err := c.Balancers.ModifyListener(ctx, &elbv2.ModifyListenerInput{
			ListenerArn:          aws.String(listener.arn),
			MutualAuthentication: &elbv2types.MutualAuthenticationAttributes{Mode: aws.String(offMode)},
		}); err != nil {
			return fmt.Errorf("stop requiring a client certificate on %s, which no hostname trusts a CA for any more: %w", listener.arn, err)
		}
	}
	store, _, err := s.readTrustStore(ctx, c)
	if err != nil {
		return err
	}
	if store != "" {
		_, err := c.Balancers.DeleteTrustStore(ctx, &elbv2.DeleteTrustStoreInput{TrustStoreArn: aws.String(store)})
		var absent *elbv2types.TrustStoreNotFoundException
		if err != nil && !errors.As(err, &absent) {
			return fmt.Errorf("delete the trust store %s, which no hostname trusts a CA in any more: %w", store, err)
		}
	}
	if err := deleteObject(ctx, c, formatBundleKey(s.state.Tier), ""); err != nil {
		return err
	}
	if recordETag == "" {
		return nil
	}
	return deleteObject(ctx, c, formatRecordKey(s.state.Tier), recordETag)
}

func deleteObject(ctx context.Context, c Clients, key, etag string) error {
	in := &s3.DeleteObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)}
	if etag != "" {
		in.IfMatch = aws.String(etag)
	}
	_, err := c.Objects.DeleteObject(ctx, in)
	var absent *s3types.NoSuchKey
	if err == nil || isConditionFailed(err) || errors.As(err, &absent) {
		return nil
	}
	return fmt.Errorf("delete %s: %w", key, err)
}

func (s *stack) readTrustStore(ctx context.Context, c Clients) (store, digest string, err error) {
	name := awsports.PublicBalancerName(s.state.Tier)
	for attempt := range trustStoreReads {
		read, err := c.Balancers.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{Names: []string{name}})
		var absent *elbv2types.TrustStoreNotFoundException
		switch {
		case errors.As(err, &absent):
			return "", "", nil
		case err != nil:
			return "", "", fmt.Errorf("read the trust store %s: %w", name, err)
		case len(read.TrustStores) > 0 && read.TrustStores[0].Status == elbv2types.TrustStoreStatusActive:
			store = aws.ToString(read.TrustStores[0].TrustStoreArn)
			digest, err := readBundleDigest(ctx, c, store)
			return store, digest, err
		}
		if err := s.pauseBeforeRead(ctx, attempt); err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("the trust store %s was not active after %d reads", name, trustStoreReads)
}

func readBundleDigest(ctx context.Context, c Clients, store string) (string, error) {
	described, err := c.Balancers.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: []string{store}})
	if err != nil {
		return "", fmt.Errorf("read which client CAs the trust store %s holds: %w", store, err)
	}
	for _, description := range described.TagDescriptions {
		for _, tag := range description.Tags {
			if aws.ToString(tag.Key) == bundleDigestTag {
				return aws.ToString(tag.Value), nil
			}
		}
	}
	return "", nil
}

func (s *stack) writeTrustStore(ctx context.Context, c Clients, store string, trusted []string, digest string) (string, error) {
	name := awsports.PublicBalancerName(s.state.Tier)
	key := formatBundleKey(s.state.Tier)
	if _, err := c.Objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(joinCertificates(trusted))),
	}); err != nil {
		return "", fmt.Errorf("write the client CAs the load balancer trusts: %w", err)
	}
	digestTag := elbv2types.Tag{Key: aws.String(bundleDigestTag), Value: aws.String(digest)}
	if store == "" {
		created, err := c.Balancers.CreateTrustStore(ctx, &elbv2.CreateTrustStoreInput{
			Name:                         aws.String(name),
			CaCertificatesBundleS3Bucket: aws.String(c.Bucket),
			CaCertificatesBundleS3Key:    aws.String(key),
			Tags:                         []elbv2types.Tag{{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")}, digestTag},
		})
		if err != nil {
			return "", refuseOverTrustQuota(fmt.Errorf("create the trust store %s: %w", name, err), len(trusted))
		}
		if len(created.TrustStores) == 0 {
			return "", fmt.Errorf("create the trust store %s: it answered no trust store", name)
		}
		active, _, err := s.readTrustStore(ctx, c)
		if err != nil {
			return "", err
		}
		if active == "" {
			return aws.ToString(created.TrustStores[0].TrustStoreArn), nil
		}
		return active, nil
	}
	if _, err := c.Balancers.ModifyTrustStore(ctx, &elbv2.ModifyTrustStoreInput{
		TrustStoreArn:                aws.String(store),
		CaCertificatesBundleS3Bucket: aws.String(c.Bucket),
		CaCertificatesBundleS3Key:    aws.String(key),
	}); err != nil {
		return "", refuseOverTrustQuota(fmt.Errorf("trust the client CAs in %s: %w", name, err), len(trusted))
	}
	if _, err := c.Balancers.AddTags(ctx, &elbv2.AddTagsInput{ResourceArns: []string{store}, Tags: []elbv2types.Tag{digestTag}}); err != nil {
		return "", fmt.Errorf("record which client CAs the trust store %s holds: %w", name, err)
	}
	return store, nil
}

func refuseOverTrustQuota(err error, trusted int) error {
	var invalid *elbv2types.InvalidCaCertificatesBundleException
	var stores *elbv2types.TooManyTrustStoresException
	switch {
	case errors.As(err, &invalid):
		return refusal.Refuse(refusal.CodeInvalid,
			"the load balancer's trust store refused the %d client CAs the hostnames behind it trust, and it holds at most as many as the quota %s allows: raise it in Service Quotas (Elastic Load Balancing), or delete the client certificates your Cloudflare zones no longer present, and deploy again (%v)",
			trusted, caQuota, err)
	case errors.As(err, &stores):
		return refusal.Refuse(refusal.CodeInvalid,
			"this account holds as many load balancer trust stores as the quota %s allows: raise it in Service Quotas (Elastic Load Balancing), or delete a trust store you no longer use, and deploy again (%v)",
			trustStoreQuota, err)
	}
	return err
}

func (s *stack) pauseBeforeRead(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	return s.pause(ctx, trustStorePause/2+rand.N(trustStorePause))
}

func pauseFor(ctx context.Context, wait time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

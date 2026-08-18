package certs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	StatusPendingValidation = string(acmtypes.CertificateStatusPendingValidation)
	StatusIssued            = string(acmtypes.CertificateStatusIssued)
)

type ACMAPI interface {
	RequestCertificate(ctx context.Context, in *acm.RequestCertificateInput, optFns ...func(*acm.Options)) (*acm.RequestCertificateOutput, error)
	DescribeCertificate(ctx context.Context, in *acm.DescribeCertificateInput, optFns ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error)
	DeleteCertificate(ctx context.Context, in *acm.DeleteCertificateInput, optFns ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error)
}

type Certificate struct {
	ARN        string        `json:"arn,omitempty"`
	Region     string        `json:"region,omitempty"`
	Status     string        `json:"status,omitempty"`
	Validation []edge.Record `json:"validation,omitempty"`
}

func (c Certificate) Issued() bool {
	return c.ARN != "" && c.Status == StatusIssued
}

type Probe struct {
	At   time.Time `json:"at,omitempty"`
	Edge edge.Kind `json:"edge,omitempty"`
	OK   bool      `json:"ok,omitempty"`
}

type Issuer struct {
	API      ACMAPI
	Region   string
	Wait     func(context.Context, time.Duration) error
	Attempts int
	Every    time.Duration
}

const (
	issueAttempts = 60
	issueEvery    = 10 * time.Second
)

func (i Issuer) attempts() int {
	return max(i.Attempts, 1)
}

func (i Issuer) every() time.Duration {
	if i.Every <= 0 {
		return issueEvery
	}
	return i.Every
}

func (i Issuer) window() time.Duration {
	return time.Duration(i.attempts()-1) * i.every()
}

func (i Issuer) hold(ctx context.Context) error {
	if i.Wait != nil {
		return i.Wait(ctx, i.every())
	}
	return waitFor(ctx, i.every())
}

func waitFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (i Issuer) Request(ctx context.Context, hostname string) (Certificate, error) {
	out, err := i.API.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(hostname),
		ValidationMethod: acmtypes.ValidationMethodDns,
		IdempotencyToken: aws.String(idempotencyToken(hostname)),
	})
	if err != nil {
		return Certificate{}, fmt.Errorf("request a certificate for %s in %s: %w", hostname, i.Region, err)
	}
	return Certificate{
		ARN:    aws.ToString(out.CertificateArn),
		Region: i.Region,
		Status: StatusPendingValidation,
	}, nil
}

func idempotencyToken(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return "ocel" + hex.EncodeToString(sum[:12])
}

func (i Issuer) AwaitValidation(ctx context.Context, cert Certificate) (Certificate, error) {
	for attempt := range i.attempts() {
		if attempt > 0 {
			if err := i.hold(ctx); err != nil {
				return cert, err
			}
		}
		described, err := i.describe(ctx, cert)
		if err != nil {
			return cert, err
		}
		cert = described
		if len(cert.Validation) > 0 || cert.Issued() {
			return cert, nil
		}
	}
	return cert, fmt.Errorf(
		"gave up after %s waiting for ACM to publish the validation record for certificate %s: nothing can be created until it does, so re-run this once ACM has caught up",
		i.window(), cert.ARN,
	)
}

func (i Issuer) AwaitIssued(ctx context.Context, cert Certificate, say func(string)) (Certificate, error) {
	for attempt := range i.attempts() {
		if attempt > 0 {
			if err := i.hold(ctx); err != nil {
				return cert, err
			}
		}
		described, err := i.describe(ctx, cert)
		if err != nil {
			return cert, err
		}
		cert = described
		switch cert.Status {
		case StatusIssued:
			say(fmt.Sprintf("Certificate issued for %s", cert.ARN))
			return cert, nil
		case string(acmtypes.CertificateStatusFailed), string(acmtypes.CertificateStatusValidationTimedOut), string(acmtypes.CertificateStatusRevoked):
			return cert, fmt.Errorf("certificate %s is %s: delete it in ACM and run this again", cert.ARN, strings.ToLower(cert.Status))
		}
	}
	return cert, fmt.Errorf(
		"gave up after %s waiting for certificate %s to be issued; it is still %s: %s, then re-run — this picks up where it left off",
		i.window(), cert.ARN, strings.ToLower(cert.Status), outstanding(cert.Validation),
	)
}

func (i Issuer) describe(ctx context.Context, cert Certificate) (Certificate, error) {
	out, err := i.API.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(cert.ARN),
	})
	if err != nil {
		return cert, fmt.Errorf("read certificate %s: %w", cert.ARN, err)
	}
	cert.Status = string(out.Certificate.Status)
	if records := validationRecords(out.Certificate.DomainValidationOptions); len(records) > 0 {
		cert.Validation = records
	}
	return cert, nil
}

func validationRecords(options []acmtypes.DomainValidation) []edge.Record {
	records := make([]edge.Record, 0, len(options))
	for _, option := range options {
		if option.ResourceRecord == nil {
			continue
		}
		records = append(records, edge.Record{
			Name:  trimRoot(aws.ToString(option.ResourceRecord.Name)),
			Type:  edge.RecordType(option.ResourceRecord.Type),
			Value: trimRoot(aws.ToString(option.ResourceRecord.Value)),
		})
	}
	return records
}

func trimRoot(name string) string {
	return strings.TrimSuffix(name, ".")
}

func outstanding(records []edge.Record) string {
	if len(records) == 0 {
		return "create the validation record ACM asks for"
	}
	wanted := make([]string, 0, len(records))
	for _, rec := range records {
		wanted = append(wanted, rec.Instruction())
	}
	return strings.Join(wanted, "; ")
}

func (i Issuer) Discard(ctx context.Context, cert Certificate, say func(string)) {
	if i.API == nil || cert.ARN == "" {
		return
	}
	if _, err := i.API.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String(cert.ARN),
	}); err != nil {
		say(fmt.Sprintf("Leaving certificate %s standing: %v — delete it in ACM once nothing uses it", cert.ARN, err))
	}
}

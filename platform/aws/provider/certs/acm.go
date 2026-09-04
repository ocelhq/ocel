package certs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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
	ListCertificates(ctx context.Context, in *acm.ListCertificatesInput, optFns ...func(*acm.Options)) (*acm.ListCertificatesOutput, error)
}

type Certificate struct {
	ARN        string        `json:"arn,omitempty"`
	Region     string        `json:"region,omitempty"`
	Status     string        `json:"status,omitempty"`
	Domains    []string      `json:"domains,omitempty"`
	Adopted    bool          `json:"adopted,omitempty"`
	Validation []edge.Record `json:"validation,omitempty"`
	NotAfter   time.Time     `json:"notAfter,omitzero"`
	Renewal    string        `json:"renewal,omitempty"`
}

const RenewalWindow = 30 * 24 * time.Hour

func (c Certificate) Renewed() bool {
	return c.Renewal == string(acmtypes.RenewalStatusSuccess)
}

func (c Certificate) ExpiringSoon(now time.Time) bool {
	return !c.NotAfter.IsZero() && !c.Renewed() && c.NotAfter.Sub(now) < RenewalWindow
}

func (c Certificate) Issued() bool {
	return c.ARN != "" && c.Status == StatusIssued
}

func (c Certificate) Covers(hostname string) bool {
	return slices.ContainsFunc(c.Domains, func(covered string) bool { return nameCovers(covered, hostname) })
}

func (c Certificate) CoversAll(hostnames []string) bool {
	for _, host := range hostnames {
		if !c.Covers(host) {
			return false
		}
	}
	return true
}

func nameCovers(covered, hostname string) bool {
	if strings.EqualFold(covered, hostname) {
		return true
	}
	under, wildcard := strings.CutPrefix(covered, "*.")
	if !wildcard {
		return false
	}
	label, ok := cutSuffixFold(hostname, "."+under)
	return ok && label != "" && !strings.Contains(label, ".")
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) < len(suffix) || !strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s, false
	}
	return s[:len(s)-len(suffix)], true
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

	releaseBudget  = 2 * time.Minute
	releaseFirst   = 2 * time.Second
	releaseLongest = 20 * time.Second
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
	return i.pause(ctx, i.every())
}

func (i Issuer) pause(ctx context.Context, d time.Duration) error {
	if i.Wait != nil {
		return i.Wait(ctx, d)
	}
	return waitFor(ctx, d)
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

func (i Issuer) Request(ctx context.Context, hostnames []string) (Certificate, error) {
	if len(hostnames) == 0 {
		return Certificate{}, errors.New("request a certificate: no hostname to request one for")
	}
	var alternates []string
	if len(hostnames) > 1 {
		alternates = hostnames[1:]
	}
	out, err := i.API.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String(hostnames[0]),
		SubjectAlternativeNames: alternates,
		ValidationMethod:        acmtypes.ValidationMethodDns,
		IdempotencyToken:        aws.String(idempotencyToken(strings.Join(hostnames, ","))),
	})
	if err != nil {
		return Certificate{}, fmt.Errorf("request a certificate for %s in %s: %w", strings.Join(hostnames, ", "), i.Region, err)
	}
	return Certificate{
		ARN:     aws.ToString(out.CertificateArn),
		Region:  i.Region,
		Status:  StatusPendingValidation,
		Domains: slices.Clone(hostnames),
	}, nil
}

func idempotencyToken(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return "ocel" + hex.EncodeToString(sum[:12])
}

func (i Issuer) AwaitValidation(ctx context.Context, cert Certificate, say func(string)) (Certificate, error) {
	for attempt := range i.attempts() {
		if attempt > 0 {
			if attempt == 1 {
				say(fmt.Sprintf("Waiting up to %s for ACM to name the record that proves you own this domain", i.window()))
			}
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
	return cert, pending{fmt.Errorf(
		"gave up after %s waiting for ACM to publish the validation record for certificate %s: nothing can be created until it does, so re-run this once ACM has caught up",
		i.window(), cert.ARN,
	)}
}

func (i Issuer) AwaitIssued(ctx context.Context, cert Certificate, say func(string)) (Certificate, error) {
	for attempt := range i.attempts() {
		if attempt > 0 {
			if attempt == 1 {
				say(fmt.Sprintf("Waiting up to %s for ACM to issue the certificate, which it does once the record resolves", i.window()))
			}
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
	return cert, pending{fmt.Errorf(
		"gave up after %s waiting for certificate %s to be issued; it is still %s: %s, then re-run — this picks up where it left off",
		i.window(), cert.ARN, strings.ToLower(cert.Status), outstanding(cert.Validation),
	)}
}

func (i Issuer) Describe(ctx context.Context, cert Certificate) (Certificate, error) {
	return i.describe(ctx, cert)
}

func (i Issuer) describe(ctx context.Context, cert Certificate) (Certificate, error) {
	out, err := i.API.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(cert.ARN),
	})
	if err != nil {
		return cert, fmt.Errorf("read certificate %s: %w", cert.ARN, err)
	}
	cert.Status = string(out.Certificate.Status)
	cert.Domains = certificateNames(aws.ToString(out.Certificate.DomainName), out.Certificate.SubjectAlternativeNames)
	cert.NotAfter = aws.ToTime(out.Certificate.NotAfter)
	cert.Renewal = ""
	if summary := out.Certificate.RenewalSummary; summary != nil {
		cert.Renewal = string(summary.RenewalStatus)
	}
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

func certificateNames(domainName string, sans []string) []string {
	names := make([]string, 0, len(sans)+1)
	if domainName != "" {
		names = append(names, domainName)
	}
	for _, san := range sans {
		if san != "" && !slices.Contains(names, san) {
			names = append(names, san)
		}
	}
	return names
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

func (i Issuer) Discard(ctx context.Context, cert Certificate, say func(string)) error {
	if i.API == nil || cert.ARN == "" {
		return nil
	}
	if cert.Adopted {
		say(fmt.Sprintf("Leaving certificate %s standing: ocel did not request it, so it is not ocel's to delete", cert.ARN))
		return nil
	}
	err := i.delete(ctx, cert)
	if InUse(err) {
		say(fmt.Sprintf("Waiting up to %s for the edge to release certificate %s", releaseBudget, cert.ARN))
		err = i.awaitRelease(ctx, cert)
	}
	if err == nil || Gone(err) {
		return nil
	}
	say(fmt.Sprintf("Leaving certificate %s standing: %v — delete it in ACM once nothing uses it", cert.ARN, err))
	if InUse(err) {
		return nil
	}
	return fmt.Errorf("delete certificate %s: %w", cert.ARN, err)
}

func (i Issuer) awaitRelease(ctx context.Context, cert Certificate) error {
	var err error
	backoff := releaseFirst
	for waited := time.Duration(0); waited < releaseBudget; {
		hold := min(backoff, releaseBudget-waited)
		if err = i.pause(ctx, hold); err != nil {
			return err
		}
		waited += hold
		if err = i.delete(ctx, cert); !InUse(err) {
			return err
		}
		backoff = min(backoff*2, releaseLongest)
	}
	return err
}

func (i Issuer) delete(ctx context.Context, cert Certificate) error {
	_, err := i.API.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String(cert.ARN),
	})
	return err
}

type pending struct{ error }

func Pending(err error) bool {
	var held pending
	return errors.As(err, &held)
}

func Gone(err error) bool {
	var missing *acmtypes.ResourceNotFoundException
	return errors.As(err, &missing)
}

func InUse(err error) bool {
	var busy *acmtypes.ResourceInUseException
	return errors.As(err, &busy)
}

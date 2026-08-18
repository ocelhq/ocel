package certs

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

func RegionOfARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

func (i Issuer) Pinned(ctx context.Context, hostname, arn string) (Certificate, error) {
	if region := RegionOfARN(arn); region != "" && region != i.Region {
		return Certificate{}, fmt.Errorf(
			"the certificate pinned for %s lives in %s, but this edge terminates TLS in %s: pin one issued in %s, or drop the pin from `certificates` and ocel requests one there — a pinned certificate is never issued or deleted here",
			hostname, region, i.Region, i.Region,
		)
	}
	cert, err := i.describe(ctx, Certificate{ARN: arn, Adopted: true})
	if err != nil {
		return Certificate{}, err
	}
	cert.Region = i.Region
	if !cert.Issued() {
		return Certificate{}, fmt.Errorf(
			"the certificate pinned for %s is %s, not %s: finish validating it in ACM and run this again — a pinned certificate is never issued or deleted here",
			hostname, strings.ToLower(cert.Status), StatusIssued,
		)
	}
	if !cert.Covers(hostname) {
		return Certificate{}, fmt.Errorf(
			"the certificate pinned for %s covers %s, which does not include %s: pin one that covers it, or drop the pin from `certificates` and ocel requests one that does",
			hostname, strings.Join(cert.Domains, ", "), hostname,
		)
	}
	return cert, nil
}

const certificatePages = 20

const certificatesPerPage = 100

func (i Issuer) Existing(ctx context.Context, hostnames []string) (Certificate, error) {
	var token *string
	for page := 0; page < certificatePages; page++ {
		out, err := i.API.ListCertificates(ctx, &acm.ListCertificatesInput{
			CertificateStatuses: []acmtypes.CertificateStatus{acmtypes.CertificateStatusIssued},
			MaxItems:            aws.Int32(certificatesPerPage),
			NextToken:           token,
		})
		if err != nil {
			return Certificate{}, fmt.Errorf("list certificates in %s: %w", i.Region, err)
		}
		for _, summary := range out.CertificateSummaryList {
			cert := Certificate{
				ARN:     aws.ToString(summary.CertificateArn),
				Region:  i.Region,
				Status:  StatusIssued,
				Domains: certificateNames(aws.ToString(summary.DomainName), summary.SubjectAlternativeNameSummaries),
				Adopted: true,
			}
			if cert.CoversAll(hostnames) {
				return cert, nil
			}
			if !aws.ToBool(summary.HasAdditionalSubjectAlternativeNames) {
				continue
			}
			described, err := i.describe(ctx, cert)
			if err != nil {
				return Certificate{}, err
			}
			described.Region = i.Region
			if described.Issued() && described.CoversAll(hostnames) {
				return described, nil
			}
		}
		if token = out.NextToken; token == nil {
			break
		}
	}
	return Certificate{}, nil
}

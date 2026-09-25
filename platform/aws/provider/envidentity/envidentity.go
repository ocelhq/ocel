package envidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
)

const callerIdentityBody = "Action=GetCallerIdentity&Version=2011-06-15"

type Signer struct {
	Config aws.Config
	Now    func() time.Time
}

func (s Signer) SignCallerIdentity(ctx context.Context) (envsource.SignedRequest, error) {
	region := s.Config.Region
	if region == "" {
		return envsource.SignedRequest{}, errors.New("no AWS region is configured, and Infisical checks the signature against that region's STS endpoint")
	}
	if s.Config.Credentials == nil {
		return envsource.SignedRequest{}, errors.New("no AWS credentials are configured to sign with")
	}
	credentials, err := s.Config.Credentials.Retrieve(ctx)
	if err != nil {
		return envsource.SignedRequest{}, fmt.Errorf("read the AWS credentials to sign with: %w", err)
	}
	endpoint := fmt.Sprintf("https://sts.%s.amazonaws.com/", region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(callerIdentityBody))
	if err != nil {
		return envsource.SignedRequest{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sum := sha256.Sum256([]byte(callerIdentityBody))
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if err := v4.NewSigner().SignHTTP(ctx, credentials, req, hex.EncodeToString(sum[:]), "sts", region, now()); err != nil {
		return envsource.SignedRequest{}, fmt.Errorf("sign sts:GetCallerIdentity: %w", err)
	}
	return envsource.SignedRequest{Method: http.MethodPost, URL: endpoint, Header: req.Header, Body: []byte(callerIdentityBody)}, nil
}

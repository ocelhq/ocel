package cloudfront

import (
	"errors"
	"fmt"

	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"
	smithy "github.com/aws/smithy-go"
)

func isNotFound(err error) bool {
	var (
		missingEntity   *cftypes.EntityNotFound
		missingDist     *cftypes.NoSuchDistribution
		missingFunction *cftypes.NoSuchFunctionExists
		missingCache    *cftypes.NoSuchCachePolicy
		missingHeaders  *cftypes.NoSuchResponseHeadersPolicy
		missingOAC      *cftypes.NoSuchOriginAccessControl
	)
	return errors.As(err, &missingEntity) ||
		errors.As(err, &missingDist) ||
		errors.As(err, &missingFunction) ||
		errors.As(err, &missingCache) ||
		errors.As(err, &missingHeaders) ||
		errors.As(err, &missingOAC)
}

func staleETag(err error) bool {
	var (
		precondition *cftypes.PreconditionFailed
		mismatch     *cftypes.InvalidIfMatchVersion
	)
	return errors.As(err, &precondition) || errors.As(err, &mismatch)
}

func staleStoreETag(err error) bool {
	var conflict *kvstypes.ConflictException
	return errors.As(err, &conflict)
}

func missingRoute(err error) bool {
	var missing *kvstypes.ResourceNotFoundException
	return errors.As(err, &missing)
}

func stillEnabled(err error) bool {
	var enabled *cftypes.DistributionNotDisabled
	return errors.As(err, &enabled)
}

func createError(what, name string, err error) error {
	var (
		tooManyDistributions *cftypes.TooManyDistributions
		tooManyAliases       *cftypes.TooManyDistributionCNAMEs
	)
	switch {
	case errors.As(err, &tooManyDistributions):
		return fmt.Errorf("create the %s %q: this account is at its CloudFront limit, so there is no room for another distribution. Every project the native edge fronts gets one distribution, and the account-wide ceiling is the \"Distributions per account\" quota for Amazon CloudFront. Open the Service Quotas console, find Amazon CloudFront, request an increase on that quota, and deploy again once AWS grants it. If you would rather not raise it, run `ocel destroy` on a project you no longer serve and the next deploy will fit: %w", what, name, err)
	case errors.As(err, &tooManyAliases):
		return fmt.Errorf("create the %s %q: this distribution already carries as many alternate domain names as CloudFront allows. Unbind a hostname this project no longer serves, or request an increase on the \"Alternate domain names (CNAMEs) per distribution\" quota for Amazon CloudFront: %w", what, name, err)
	case throttled(err):
		return fmt.Errorf("create the %s %q: CloudFront is rate-limiting this account's control-plane calls, and the SDK gave up after its own retries. This is a throttle, not a quota, so nothing needs to be raised or deleted: wait a few seconds and deploy again. If you are deploying several projects at once, deploy them one at a time: %w", what, name, err)
	}
	return fmt.Errorf("create the %s %q: %w", what, name, err)
}

func aliasError(hostname, id string, err error) error {
	var taken *cftypes.CNAMEAlreadyExists
	if errors.As(err, &taken) {
		return fmt.Errorf("serve %s from distribution %s: another CloudFront distribution in some AWS account already claims that hostname, and CloudFront lets only one distribution answer for it. If the claim is yours, remove the hostname from that distribution and deploy again; if it is not, CloudFront will not tell you whose it is, and AWS Support is the only way to release it: %w", hostname, id, err)
	}
	return fmt.Errorf("serve %s from distribution %s: %w", hostname, id, err)
}

func throttled(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "Throttling", "ThrottlingException", "TooManyRequests", "RequestThrottled", "SlowDown":
		return true
	}
	return false
}

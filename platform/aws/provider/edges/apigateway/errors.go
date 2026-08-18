package apigateway

import (
	"errors"
	"fmt"
	"strings"

	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

func isNotFound(err error) bool {
	var missing *agtypes.NotFoundException
	return errors.As(err, &missing)
}

func createAPIError(name string, err error) error {
	if atAPICeiling(err) {
		return fmt.Errorf("create REST API %q: this account is at its API Gateway limit in this region, so there is no room for another one. Every Ocel deployment the none edge fronts gets its own regional REST API, and the account-wide ceiling is the \"Regional APIs per Region per account\" quota for Amazon API Gateway. Open the Service Quotas console in this region, find Amazon API Gateway, request an increase on that quota, and re-run the deploy once AWS grants it. If you would rather not raise it, delete the REST APIs left behind by projects you no longer serve - `ocel destroy` on a retired project removes its API - and the next deploy will fit: %w", name, err)
	}
	if throttled(err) {
		return fmt.Errorf("create REST API %q: API Gateway is rate-limiting this account's control-plane calls, and the SDK gave up after its own retries. This is a throttle, not the account's API ceiling, so nothing needs to be raised or deleted: wait a few seconds and re-run the deploy. API Gateway allows roughly one CreateRestApi per second per account, so if you are deploying several projects at once, deploy them one at a time: %w", name, err)
	}
	return fmt.Errorf("create REST API %q: %w", name, err)
}

func atAPICeiling(err error) bool {
	var exhausted *agtypes.LimitExceededException
	if !errors.As(err, &exhausted) {
		return false
	}
	return strings.Contains(strings.ToLower(exhausted.ErrorMessage()), "maximum number of")
}

func throttled(err error) bool {
	var (
		exhausted *agtypes.LimitExceededException
		tooMany   *agtypes.TooManyRequestsException
	)
	return errors.As(err, &exhausted) || errors.As(err, &tooMany)
}

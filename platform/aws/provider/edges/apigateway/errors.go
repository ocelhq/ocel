package apigateway

import (
	"errors"
	"fmt"
	"strings"

	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
)

const routingRuleQuota = 50

func isNotFound(err error) bool {
	var (
		missing   *agtypes.NotFoundException
		missingV2 *agv2types.NotFoundException
	)
	return errors.As(err, &missing) || errors.As(err, &missingV2)
}

func createAPIError(name string, err error) error {
	if atAPICeiling(err) {
		return fmt.Errorf("create REST API %q: this account is at its API Gateway limit in this region, so there is no room for another one. Every Ocel deployment the %q edge fronts gets its own regional REST API, and the account-wide ceiling is the \"Regional APIs per Region per account\" quota for Amazon API Gateway. Open the Service Quotas console in this region, find Amazon API Gateway, request an increase on that quota, and re-run the deploy once AWS grants it. If you would rather not raise it, delete the REST APIs left behind by projects you no longer serve - `ocel destroy` on a retired project removes its API - and the next deploy will fit: %w", name, Kind, err)
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
		tooManyV2 *agv2types.TooManyRequestsException
	)
	return errors.As(err, &exhausted) || errors.As(err, &tooMany) || errors.As(err, &tooManyV2)
}

func routeError(domain, host, api string, err error) error {
	var conflict *agv2types.ConflictException
	if errors.As(err, &conflict) {
		return fmt.Errorf("route %s on %s to REST API %s: another deploy claimed the routing-rule priority this one probed for, %d times running. %s is shared by every preview in this account, so concurrent deploys race for it; re-run this deploy: %w", host, domain, api, hostRuleAttempts, domain, err)
	}
	if throttled(err) {
		return fmt.Errorf("route %s on %s to REST API %s: API Gateway is rate-limiting this account's control-plane calls, and the SDK gave up after its own retries. Nothing needs to be raised or deleted: wait a few seconds and re-run the deploy: %w", host, domain, api, err)
	}
	return fmt.Errorf("route %s on %s to REST API %s: %s. %w", host, domain, api, routingRuleQuotaAdvice(domain), err)
}

func crowdedWildcardError(domain, host, api string) error {
	return fmt.Errorf("route %s on %s to REST API %s: every routing-rule priority on %s is taken, so this preview cannot be told apart from the ones already on it. %s", host, domain, api, domain, routingRuleQuotaAdvice(domain))
}

func routingRuleQuotaAdvice(domain string) string {
	return fmt.Sprintf("Each live preview holds one routing rule on %s, plus the one catch-all that answers hostnames no preview claims, and API Gateway allows %d rules per domain name by default - the \"RoutingRules Per Domain Name\" quota. Remove previews you no longer serve with `ocel preview rm`, or open the Service Quotas console in this region, find Amazon API Gateway, and request an increase on that quota", domain, routingRuleQuota)
}

func missingWildcardError(domain, host, api string) error {
	return fmt.Errorf("route %s on %s to REST API %s: %s is not a custom domain name in this account, so nothing terminates TLS for the previews it fronts. Claim it with `ocel domain use --preview %s`, then deploy again", host, domain, api, domain, strings.TrimPrefix(domain, "*."))
}

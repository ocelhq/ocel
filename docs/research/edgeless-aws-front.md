# Edge-less on AWS: API Gateway HTTP API front vs raw Function URLs

## Short answer

- HTTP API is the only regional AWS front that gives `none` mode a custom domain without CloudFront: Regional custom domain names, ACM cert in the API's region, TLS 1.2 only, wildcard `*.example.com` as one domain resource; DNS is a CNAME or Route 53 alias to the `d-xxxx.execute-api` target. Function URLs have no custom-domain surface — the hostname is a fixed `<url-id>.lambda-url.<region>.on.aws`; AWS's documented path to a domain on a FURL is CloudFront, i.e. native mode.
- Hostname → deployment inside one wildcard domain is impossible on HTTP API: API mappings key on path only, and Host-header routing rules target REST APIs only. So previews under `*.preview.example.com` on HTTP API mean one API/stage catches every host and the Lambda dispatches on `Host` (payload 2.0 carries it), or one custom domain name per preview (120/region default, `CreateDomainName` 1 per 30 s).
- The pointer flip is one synchronous control-plane write with no published propagation bound: `UpdateIntegration` on an auto-deploy `$default` stage, `UpdateStage` of a stage variable naming the Lambda, `UpdateApiMapping` to another API/stage, or Lambda `UpdateAlias` behind a FURL. AWS publishes no SLA for any of them; print "seconds, unpublished", never "instant".
- FURL-on-alias flips require restructuring: a URL attaches to `$LATEST` or an alias only, so one function per pointer with published, immutable versions per deployment — today Ocel builds one function per release with a per-release URL.
- What HTTP API costs Next: 30 s hard integration timeout, 10 MB payload, 10 KB request-line+headers, no response streaming (buffered `Invoke`; PPR/Suspense streaming degrade to full-render delivery), no WAF, no per-client throttling, $1.00/M requests. Raw FURLs keep streaming (200 MB, 15 min) and 1 MB headers but expose the function with `AuthType: NONE` and a public resource policy, no WAF, throttling only via reserved concurrency (429), no domain, no charge beyond Lambda.
- REST API regional is the AWS front that has all three — Host routing rules on a wildcard domain, response streaming, integration timeout raisable past 29 s, WAF — at $3.50/M.

## 1. HTTP API custom domains

- Regional custom domain names serve REST and HTTP APIs; TLS 1.2 is the only minimum version; the DNS record is the operator's job. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-custom-domain-names.html
- The ACM certificate must be in the same region as the API; creating the domain creates the `AWSServiceRoleForAPIGateway` service-linked role; the record is a Route 53 A alias (`regionalDomainName` + `regionalHostedZoneId`) or a CNAME for third-party DNS. https://docs.aws.amazon.com/apigateway/latest/developerguide/apigateway-regional-api-custom-domain-create.html
- Wildcard: `*` as the first label; needs an ACM cert validated by DNS or email; `*.example.com` and `a.example.com` may coexist in one account; a wildcard cannot be created if another account already owns a conflicting name; a custom domain name is unique per region across all accounts. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-custom-domain-names.html#http-wildcard-custom-domain-names, https://docs.aws.amazon.com/apigateway/latest/developerguide/how-to-custom-domains.html#custom-domain-considerations
- Route 53 wildcard records must replace the leftmost label; specific names win; an ACM wildcard cert covers one label only. https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/DomainNameFormat.html#domain-name-format-asterisk
- The default `execute-api` endpoint can be disabled per API (all stages), effective without redeploy on auto-deploy stages. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-disable-default-endpoint.html
- Mutual TLS is available on Regional custom domains with a truststore in S3. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-mutual-tls.html
- Quotas: 120 custom domains per account per region (adjustable); `CreateDomainName`/`UpdateDomainName`/`DeleteDomainName` 1 request per 30 s per account (fixed); 200 multi-level API mappings per domain. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/limits.html

## 2. Routing hostnames to deployments

- API mappings connect (domain, path key) → (API, stage); selection is longest matching path; HTTP and REST stages may share a domain; multi-level mappings need TLS 1.2. Nothing keys on Host. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-mappings.html
- Routing rules can match `Host` on a wildcard domain and send subdomains to different targets, but "WebSocket or HTTP APIs aren't supported as target APIs for routing rules", and mixing REST and HTTP mappings on a domain disables rules. https://docs.aws.amazon.com/apigateway/latest/developerguide/rest-api-routing-mode.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/rest-api-routing-rules-examples.html#rest-api-routing-rules-examples-rule-for-wildcard-domains
- Inside a Lambda proxy integration the original host is available: payload 2.0 `headers` and `requestContext.domainName` ("should be the same as the incoming Host header"). https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html
- Stage variables may stand in for the Lambda function name or alias in the integration URI (`function:${stageVariables.fn}` or `function:name:${stageVariables.alias}`); permissions for the resolved function are added manually; 10 stages per API (adjustable), 100 variables per stage, value ≤ 512 chars. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.stage-variables-reference.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.stage-variables.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html
- Per-API quotas: 300 routes (adjustable), 300 integrations (fixed). No HTTP-API-count quota is published on the HTTP API quotas page; REST lists 600 regional APIs per region. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-execution-service-limits-table.html

Variants that follow:
- One API, `$default` auto-deploy stage, one `$default` route → integration per pointer flip (`UpdateIntegration`); production only, previews need their own API or their own domain.
- One API, one stage per pointer, integration URI through a stage variable, one API mapping per custom domain name per pointer: capped by 10 stages/API and 120 domains/region, and by the 30 s domain-create spacing.
- One API per pointer, `UpdateApiMapping` (`apiId`, `stage`) on the pointer's domain. https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/domainnames-domainname-apimappings-apimappingid.html
- One wildcard domain, one API, Host-dispatch in the origin Lambda (the ledger lookup the Cloudflare worker does today moves into the function or a router function in front).

## 3. Flip latency and semantics

- HTTP API: "You must deploy an API for changes to take effect. If you enable automatic deployments, changes to an API are automatically released for you." No propagation time is stated anywhere in the HTTP API guide. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.html
- Control-plane budget: "Total operations 10 requests per second with a burst quota of 40" per account. https://docs.aws.amazon.com/apigateway/latest/developerguide/limits.html
- Lambda alias: "A Lambda alias is a pointer to a function version that you can update"; `UpdateAlias` takes `FunctionVersion` and an optional `RevisionId` for optimistic locking; no consistency statement. Control plane is 15 rps across all non-invoke APIs. https://docs.aws.amazon.com/lambda/latest/dg/configuration-aliases.html, https://docs.aws.amazon.com/lambda/latest/api/API_UpdateAlias.html, https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html#api-requests
- No SLA exists to print for any of these; the honest bound is "unpublished; observed as seconds", to be measured, not quoted.

## 4. Function URLs

- URL is generated, never changes, `https://<url-id>.lambda-url.<region>.on.aws`; attachable to `$LATEST` or an alias only (`Qualifier` = "The alias name"); public internet only, dual-stack. https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html, https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunctionUrlConfig.html
- Custom domain: no such setting exists on the URL config; Lambda's own decision guide lists "custom domain names" among the reasons to choose API Gateway; CloudFront's guide is where a FURL gets an alternate domain name (OAC requires `AWS_IAM`). https://docs.aws.amazon.com/lambda/latest/dg/furls-http-invoke-decision.html, https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-lambda.html
- Auth: `NONE` skips IAM but the resource policy must grant `lambda:InvokeFunctionUrl` and `lambda:InvokeFunction` to `*` (new URLs need both since October 2025); `AWS_IAM` needs SigV4 on every request; the recommended `lambda:InvokedViaFunctionUrl` condition keeps the public grant URL-only. https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html
- Throttling: only via reserved concurrency (max RPS = 10 × reserved; excess → 429); the emergency off switch is reserved concurrency 0. https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html#urls-throttling
- WAF associates with CloudFront, ALB, API Gateway REST API, AppSync, Cognito, App Runner, AgentCore Gateway, Verified Access, Amplify — neither HTTP APIs nor Function URLs. https://docs.aws.amazon.com/waf/latest/developerguide/how-aws-waf-works-resources.html
- Limits: 6 MB request/response buffered, 200 MB streamed, 900 s timeout, 1 MB request line + headers, streaming uncapped for 6 MB then 2 MB/s. https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html
- Payload schema equals API Gateway 2.0. https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html
- Deleting a function deletes its URL asynchronously; recreating the same name at once may inherit the old URL. https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html
- Alias model: a published version freezes code, env vars, memory, layers, timeout; `$LATEST` is the only mutable target; publishing needs a change. https://docs.aws.amazon.com/lambda/latest/dg/configuration-versions.html

## 5. Streaming and timeouts

- HTTP API: integration timeout 30 s fixed, payload 10 MB, request line + headers 10240 bytes; response streaming "No" for HTTP APIs. https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-vs-rest.html
- REST API: response streaming for all endpoint types via `AWS_PROXY`, up to 15 min, 5 min idle on regional, first 10 MB uncapped then 2 MB/s, no caching/encoding/VTL under STREAM; integration timeout raisable above 29 s at the cost of the region throttle quota; WAF supported. https://docs.aws.amazon.com/apigateway/latest/developerguide/response-transfer-mode.html, https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-execution-service-limits-table.html
- Lambda streaming reaches clients through function URLs, `InvokeWithResponseStream`, or the API Gateway (REST) proxy; Node.js managed runtimes only. https://docs.aws.amazon.com/lambda/latest/dg/configuration-response-streaming.html
- Next.js: streaming needs the whole path to pass chunks; "AWS ALB with Lambda integration may buffer"; PPR without streaming delivers shell and dynamic content together. https://nextjs.org/docs/app/guides/self-hosting#streaming-and-suspense
- Prices: HTTP API $1.00/M (first 300M), REST $3.50/M, data out $0.09/GB; FURL has no per-request charge beyond Lambda. https://aws.amazon.com/api-gateway/pricing/, https://docs.aws.amazon.com/lambda/latest/dg/furls-http-invoke-decision.html

## 6. Where Ocel stands today

- Every deployment is its own function with the release token in the physical name, and its own URL with `AWS_IAM` + `RESPONSE_STREAM`. pkg/naming/coordinate.go:94-101, platform/aws/provider/deploy/function.go:41-62, :226-228, :464-476
- The Cloudflare edge resolves route → URL per deployment and signs with edge credentials. platform/aws/provider/deploy/edgeworker.go:158-198, platform/aws/provider/deploy/production.go:685-693
- Bootstrap's shared optimizer and revalidator are also FURL + `RESPONSE_STREAM`, IAM-invoked. platform/aws/provider/bootstrap/optimizer.go:97-118
- The entrypoint serves the invoke through a local HTTP server behind the membrane layer; buffered payload 2.0 from HTTP API is schema-compatible with what the URL sends but loses the stream. platform/aws/functions/entrypoints/src/next/entrypoint.mts:24-58

## 7. Previews per variant

- HTTP API + wildcard domain: `*.preview.example.com` → one API/stage; the origin dispatches on Host from a ledger; every preview shares the 30 s / 10 MB / buffered envelope; new previews cost nothing on the front.
- HTTP API + domain per preview: a `CreateDomainName` per preview (30 s spacing, 120 cap) plus a mapping; flip = `UpdateApiMapping`.
- HTTP API + one API per preview: mapping per preview under one domain by path only, so no host-per-preview without domains.
- Raw FURL: each preview is a URL on its own alias (or its own function as today); the URL is the identity, unbrandable, `NONE` if browsers hit it directly, `AWS_IAM` otherwise.

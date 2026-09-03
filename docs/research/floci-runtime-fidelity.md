# What floci can serve once an app is deployed to it

Research for [#831](https://github.com/ocelhq/ocel/issues/831), part of the test-suite map [#830](https://github.com/ocelhq/ocel/issues/830).

**Verdict: an aws PR journey can reach the contract.** A deployed Lambda answers real HTTP on floci through a Function URL, through API Gateway v2, and through CloudFront, and the function body is executed in a real `public.ecr.aws/lambda/nodejs:22` container. The harness needs three concrete accommodations, listed under "What the harness must do".

Two premises in the ticket are wrong and are corrected below: floci is not LocalStack-based, and LocalStack Community no longer exists.

## Method

Two sources, kept separate:

- **Docs** — floci's own repo and site (`github.com/floci-io/floci`, `floci.io`). Cited inline.
- **Live** — a `floci/floci:latest` container (v2.0.1, image digest `sha256:4e451c39…86e8eb`, built 2026-09-01) run the way `scripts/floci.sh` runs it: `-p 127.0.0.1::4566 -v /var/run/docker.sock:/var/run/docker.sock`. Every line marked **Live** was observed on 2026-09-03 against that container with `aws` CLI and `curl`.

Claims are labelled. Where docs and experiment disagree, the experiment wins and says so.

## floci is not LocalStack

`docker image inspect floci/floci:latest`, **Live**:

- `org.opencontainers.image.description` = "Local AWS emulator — open-source alternative to LocalStack Community"
- `org.opencontainers.image.source` = `https://github.com/floci-io/floci`, licence MIT, version 2.0.1
- entrypoint `/usr/local/bin/docker-entrypoint.sh`, command `/app/application -Dquarkus.http.host=0.0.0.0` on ubi9-minimal — a Quarkus/GraalVM native binary, not a Python LocalStack
- the image copies `docker/localstack-parity.sh`

floci is an independent reimplementation of the AWS wire protocols in Java/Quarkus ([README](https://raw.githubusercontent.com/floci-io/floci/main/README.md)). Its LocalStack relationship is a compatibility shim only: `/etc/localstack/init/` scripts still run, `/_localstack/health` and `/_localstack/init` are still served, and the log still ends with LocalStack's `Ready.` line so Testcontainers' `LocalStackContainer` wait strategy works. `LOCALSTACK_PARITY=false` turns that off. That shim is why `scripts/floci.sh` greps `/_localstack/health` and why the emulator looked LocalStack-shaped.

floci's own stated motivation: LocalStack's community edition sunset in March 2026, requiring auth tokens. Confirmed from LocalStack's side — the tiers are now Hobby / Base / Ultimate ([pricing](https://www.localstack.cloud/pricing), [road-ahead post](https://blog.localstack.cloud/the-road-ahead-for-localstack/)), Hobby is free but **non-commercial only**, and CloudFront, API Gateway v2 and ECR are all Base-and-up. All three are on ocel's aws path, so LocalStack is not a live alternative for this repo's CI regardless of fidelity.

`GET /_floci/health` reports `"edition":"community"`, `"original_edition":"floci-always-free"` and ~100 services `"running"` (**Live**). Those two edition strings appear nowhere in floci's docs; treat them as vestigial. The `running` list is a declared surface, not a fidelity claim — floci grades fidelity separately, in the README's "Detailed service notes" table and the [per-service pages](https://floci.io/floci/services/), which mark stubs as stubs.

## Service-by-service: execute or record

| Service | Verdict | Evidence |
| --- | --- | --- |
| Lambda (zip) | **Executes** | **Live**: `create-function` → `Active`; `invoke` returned the handler's real payload; `docker ps` showed a sibling container `floci-expfn-2d3f23b7` running `public.ecr.aws/lambda/nodejs:22` |
| Lambda layers | **Executes** | **Live**: a published layer's `node_modules` was `require`-able from the handler; invoke returned `from-layer` |
| Function URLs | **Executes** | **Live**, see below |
| API Gateway v2 | **Executes, routes to Lambda** | **Live**, see below |
| CloudFront | **Executes, really proxies** | **Live**: a distribution with an `example.com` custom origin returned example.com's actual HTML, `cf-cache-status: HIT`, `Server: cloudflare` |
| CloudFormation | **Executes** | **Live**: a stack with `AWS::S3::Bucket` reached `CREATE_COMPLETE` and the bucket really existed |
| S3 | **Executes** | **Live**: put and a plain path-style `GET http://127.0.0.1:PORT/bucket/key` returned the object |
| DynamoDB | Executes | Docs: real in-process implementation, 28 ops, GSI/LSI/TTL/transactions/Streams |
| SSM Parameter Store | Executes, **but see SecureString** | **Live**: put/get round-trips |
| KMS | Executes crypto | Docs: real Encrypt/Decrypt/Sign/Verify. Grants are stored but **never evaluated** during crypto ops |
| CloudWatch Logs | Executes | **Live**: `/aws/lambda/expfn` log group and a `2026/09/03/[$LATEST]…` stream appeared on their own |
| IAM | **Records only, by default** | **Live**, see below |
| ECR | Executes, real registry | **Live**: floci spawned a sibling `floci-ecr-registry` (`registry:2`) published on host `:5100`; `docker push` to it succeeded |
| Lambda (image) | Attempts for real; blocked by the host docker daemon | **Live**, see below |

## Function URLs

**Live.** `create-function-url-config --auth-type NONE` returned:

```
http://81f5e06d6e3e348a9012834dae54596d.lambda-url.us-east-1.localhost:4566/
```

That host resolves to `::1` in a normal resolver, but **the port `4566` is baked into the string** — so the URL is only directly usable when floci's 4566 is published on host port 4566. `scripts/floci.sh` publishes an ephemeral port (`-p 127.0.0.1::4566`), so the URL as handed back does not work under it.

Both of these do work, on whatever port floci is published on:

```
curl -H "Host: <id>.lambda-url.us-east-1.localhost" http://127.0.0.1:$PORT/probe
```

and floci additionally routes `/{proxy:.*}` under its Lambda URL controller into the normal `Invoke` path ([lambda docs](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/lambda.md)).

The response was a genuine handler execution with a genuine API Gateway v2 event: `rawPath`, `requestContext.http.method` and `version: "2.0"` were all correct, and the function's environment variables were present.

`--auth-type AWS_IAM` is **not enforced** (**Live**): an unsigned `curl` against an `AWS_IAM` Function URL returned 200 and the body. That is convenient for a harness — no SigV4 needed — and a fidelity gap worth knowing.

### The one real trap: `RESPONSE_STREAM` drops the body

**Live.** A handler wrapped in `awslambda.streamifyResponse` + `HttpResponseStream.from`, behind a `RESPONSE_STREAM` Function URL, answered:

```
HTTP/1.1 200 OK
content-type: text/plain
content-length: 0
```

The status and headers from the stream prelude survived; **the streamed chunks were dropped**. A direct `lambda invoke` of the same function returned the full raw envelope (prelude JSON, NUL, `chunk-1\nchunk-2\n`), so the handler is fine — floci's Function URL controller does not decode the streaming envelope. This matches floci's docs listing `InvokeWithResponseStream` as not implemented.

This matters because `platform/aws/provider/deploy/transformplan.go:115` sets `InvokeMode: RESPONSE_STREAM` for app function URLs by default.

**It is survivable.** A *plain, non-streamified* handler behind a `RESPONSE_STREAM` URL returns its body in full (**Live**, verified explicitly). ocel's app handler is buffered — nothing under `frameworks/`, `packages/` or the aws function payloads calls `streamifyResponse` except `platform/aws/functions/image-optimizer`. So:

- ordinary app routes answer correctly on floci
- **`platform/aws/functions/image-optimizer` will return an empty body on floci**, because it genuinely streams
- any future genuinely-streaming framework route will do the same

## API Gateway v2

**Live.** With an `AWS_PROXY` integration on `ANY /{proxy+}` and a `$default` auto-deploy stage, all three of these returned the handler's real JSON with `200`:

```
curl -H "Host: <apiId>.execute-api.us-east-1.localhost"     http://127.0.0.1:$PORT/hi
curl -H "Host: <apiId>.execute-api.us-east-1.amazonaws.com" http://127.0.0.1:$PORT/hi
curl                                                        http://127.0.0.1:$PORT/_aws/execute-api/<apiId>/hi
```

as did the v1-style path form `/restapis/<apiId>/$default/_user_request_/hi`. Note the `amazonaws.com` host works — floci matches on the api id, not the suffix — which makes a harness's URL construction trivial.

`create-api --target <lambda arn>` alone did **not** produce a routable API (404, **Live**); the integration, route and stage have to be created explicitly. floci also supports **custom API ids**, which would let a harness form URLs without reading them back ([api-gateway docs](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/api-gateway.md)).

Docs caveat: v1 REST supports only `AWS_PROXY`, `AWS`+VTL and `MOCK` — no `HTTP_PROXY`.

## CloudFront

**Live.** A distribution created with a custom origin went straight to `Status: Deployed`, and

```
curl -H "Host: E3BFK8YTDISNIY.cloudfront.net" http://127.0.0.1:$PORT/
```

returned example.com's real HTML through the origin. `{id}.cloudfront.net` does **not** resolve in DNS, so the `Host` header override is mandatory from the host machine.

Docs limits ([cloudfront](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/cloudfront.md)): only **GET / HEAD / OPTIONS** are proxied — a POST through a distribution is not described as working, which would bite a contract that exercises writes through the edge. Invalidations complete instantly. Signed URLs/cookies with `TrustedKeyGroups` are genuinely enforced.

## Container / image Lambda

**Docs**: supported. floci's Lambda runner rewrites real-AWS-shaped `<account>.dkr.ecr.<region>.amazonaws.com/...` URIs to its loopback registry at pull time ([ecr docs](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/ecr.md)).

**Live**: the rewrite happens and floci really tries to start a container — `create-function --package-type Image` with an `…amazonaws.com/imgfn:latest` URI reached `Active`, and invoking it produced

```
Failed to start Lambda container: … failed to resolve reference
"000000000000.dkr.ecr.us-east-1.localhost:5100/imgfn:latest":
… http: server gave HTTP response to HTTPS client
```

So floci did its half. The failure is the **host docker daemon**: `docker info` showed no insecure registries, and a bare `docker pull` of the same reference failed identically. Docker's automatic insecure-registry allowance covers `localhost` and `127.0.0.0/8`, not a `*.localhost` subdomain, so the runner cannot pull unless the daemon has `"insecure-registries": ["000000000000.dkr.ecr.us-east-1.localhost:5100"]` (or the `.localhost` suffix) in `daemon.json`.

Two things blunt this. First, `scripts/floci.sh` does not publish 5100 at all — floci publishes it itself, from the sibling registry container. Second, `platform/aws/provider/deploy/function.go` builds only zip-package functions (`S3Bucket`/`S3Key`, `PackageType` never set), so **ocel emits no image Lambdas today** and the map already records aws container compute as red. This is a fog item for when that lands, not a blocker now.

Also documented: `FLOCI_SERVICES_LAMBDA_EXECUTOR=kubernetes` runs functions as pods for CI without a docker socket, but image-package URIs are then passed to the kubelet unchanged and floci's ECR is not pullable by cluster nodes.

## Security posture is permissive, and one gap is load-bearing

**IAM is not enforced by default.** **Live**: a user with an explicit `Deny s3:*` policy, using its own access key, successfully listed buckets. [Docs](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/iam.md): *"By default Floci accepts any credentials without enforcing IAM policies."* `FLOCI_SERVICES_IAM_ENFORCEMENT_ENABLED=true` turns on a real evaluator (explicit-deny-wins, boundaries, optional SCPs), but even then a request with no `Authorization` header is allowed, and an unresolvable action maps to allow. S3 bucket policies need a *second* flag, `FLOCI_SERVICES_S3_ENFORCE_AUTH`, also default off.

**SSM `SecureString` is stored in plaintext.** **Live**: `get-parameter` *without* `--with-decryption` returned the secret value verbatim. Docs confirm: *"the value is not encrypted at rest."*

Consequence for the suite: floci can prove that a deploy *works*, never that it is *locked down*. Anything in the contract asserting "the edge cannot reach this without the origin secret", "this Function URL refuses an unsigned caller", or "this parameter is encrypted" is untestable on floci and belongs on the dispatch-only real-aws run.

**One CI trap worth a flag.** Unsupported CloudFormation resource types are **silently stubbed** — assigned `arn:aws:stub:::<logicalId>` while the stack still reaches `CREATE_COMPLETE`, with only a WARN log ([cloudformation docs](https://raw.githubusercontent.com/floci-io/floci/main/docs/services/cloudformation.md)). Set `FLOCI_SERVICES_CLOUDFORMATION_ALLOW_STUB_UNSUPPORTED_RESOURCE_TYPES=false` in CI to get `CREATE_FAILED` and a rollback instead. `ValidateTemplate` and `SetStackPolicy` are stubs too.

## What the harness must do

1. **Publish 4566 as 4566**, or override `Host`. Function URLs come back with `:4566` hardcoded. `scripts/floci.sh` publishes an ephemeral port, so a journey either binds `-p 127.0.0.1:4566:4566` (one emulator per runner, no parallelism) or rewrites the URL it reads back into `Host: <host-part>` + `http://127.0.0.1:$OCEL_FLOCI_ENDPOINT_PORT`. The `Host`-override route keeps the existing per-suite-container isolation and is the cheaper change.
2. **Override `Host` for CloudFront too.** `{id}.cloudfront.net` has no DNS.
3. **Expect no body from anything genuinely streaming** — today that is only `platform/aws/functions/image-optimizer`. Contract assertions over Next image optimisation are red on floci.

And two ceilings to record as expectations rather than fix: CloudFront proxies GET/HEAD/OPTIONS only, so a write through the edge is red; and nothing about IAM, origin-secret enforcement or secret encryption is meaningful on floci.

## Open

- Whether floci's CloudFront honours ocel's origin-secret custom header end to end (docs say origin custom headers persist and replace same-named viewer headers; untested here).
- Whether a real `ocel deploy` — Pulumi, not raw API calls — lands cleanly on floci. The existing `TestE2E*` suite proves bootstrap does; nothing yet proves the app stack does. That is the natural next prototype and the last thing standing between this finding and a green aws journey cell.

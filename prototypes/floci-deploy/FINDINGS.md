# An ocel deploy of a serverless app lands and answers on floci

Prototype for [#847](https://github.com/ocelhq/ocel/issues/847), part of the test-suite map [#830](https://github.com/ocelhq/ocel/issues/830). Throwaway branch; the configs beside this file are the fixture that was deployed.

**Verdict: the deploy lands, the app does not answer.** A real `ocel deploy` (Pulumi, `@ocel/provider-aws`) of a plain express app bootstraps, deploys, redeploys, rolls back and destroys cleanly on floci once one ocel bug is fixed, but no HTTP request reaches the app: floci cannot deliver ocel's streamed responses, and neither of the two aws edges routes on floci. Every aws serverless cell is red on the PR target until floci implements response streaming.

## Method

- floci `floci/floci:latest` v2.0.1 (image `4e451c39c7bb`), run as `scripts/floci.sh` runs it (ephemeral host port, docker socket mounted). Three containers: one default, two with `FLOCI_SERVICES_CLOUDFORMATION_ALLOW_STUB_UNSUPPORTED_RESOURCE_TYPES=false`.
- `ocel` and the provider's `deploy` binary built from `main` at `78b33b66` with `go build`; the provider rebuilt once with the fix below.
- Fixture: `examples/expr` (a no-SDK express app, the shape the map calls `hello-express`) with the configs in this directory, `--config ocel.aws.config.ts` (CloudFront, the default edge) and `--config ocel.aws-apigw.config.ts`. Env: `AWS_ENDPOINT_URL=<floci>`, `AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, `AWS_REGION=us-east-1`, `OCEL_NO_BROWSER=1`, no console token.
- Every line below was observed on 2026-09-03.

## The journey, step by step

| Step | CloudFront edge (default) | api-gateway edge |
| --- | --- | --- |
| `ocel bootstrap production --yes` | green: both stacks `CREATE_COMPLETE`, buckets, tables, KMS key, two SSM params real | green |
| bootstrap again to add a feature | **red** [#853](https://github.com/ocelhq/ocel/issues/853): `DescribeChangeSet ... Stack with id null does not exist` | same |
| `ocel deploy --yes` | **red** [#852](https://github.com/ocelhq/ocel/issues/852): Edge stage `CreateDistribution ... NoSuchResponseHeadersPolicy`, app stack never runs | **red then green**: first fails in the Pulumi state backend (ocel bug, fixed on this branch, below); then refuses until `ocel domain add`; then 8 resources created in 7s |
| `ocel domain add` | refuses: "no production deploys yet" (correct: the deploy never landed) | green: ACM cert issued, custom domain bound, mapping to stage `live` |
| GET through the Function URL | n/a | **red** [#851](https://github.com/ocelhq/ocel/issues/851): 200 + headers + `Content-Length: 10`, body never arrives, hangs to timeout |
| GET through the edge | n/a | **red** [#854](https://github.com/ocelhq/ocel/issues/854): 400 `FunctionName contains invalid characters: ${stageVariables.entry}` |
| addresses as printed | n/a | **red** [#855](https://github.com/ocelhq/ocel/issues/855): `:4566` baked into the Function URL; `appUrls` is the unresolvable declared hostname |
| `ocel env set` / `env ls` | green | green |
| `ocel deploy --yes --tag v2` | n/a | green, second promotion active |
| `ocel deployments ls` | n/a | green, both promotions listed |
| `ocel rollback` | n/a | green: "Rolled back to promotion 5817b3…" |
| `ocel destroy production --yes` | n/a | green in 16s; only the shared `ocel-not-found-production` REST API remains |

## What the app stack proved

Pulumi's `aws` provider plugin (v7.36.0, terraform-aws underneath) honours the ambient `AWS_ENDPOINT_URL` with no per-service endpoint config: IAM role, role policies, S3 artifact object, Lambda layer, Lambda function and Function URL all created against floci, and `pulumi destroy` removed them. The membrane layer upload and the artifact upload went to floci's S3. `nodejs24.x` was pulled as `public.ecr.aws/lambda/nodejs:24` and the handler ran ("Server is running..." in CloudWatch Logs). A direct `aws lambda invoke` with an API Gateway v2 event returned the full response envelope: prelude JSON, eight NUL bytes, `{"id":"7"}`.

## The one ocel bug: the Pulumi state backend went to real AWS

```
select stack prod--infra: read ".pulumi/meta.yaml": InvalidAccessKeyId:
The AWS Access Key Id you provided does not exist in our records. status code: 403
```

`naming.StateBackendURL` hands Pulumi a bare `s3://bucket/project`; Pulumi's gocloud blob client resolves that against `s3.amazonaws.com` regardless of `AWS_ENDPOINT_URL`. Verified by hand: `pulumi login s3://bucket/x` fails the same way, `pulumi login "s3://bucket/x?endpoint=127.0.0.1:PORT&disableSSL=true&s3ForcePathStyle=true"` logs in and writes `meta.yaml` into floci's bucket. The fix on this branch appends those keys when `AWS_ENDPOINT_URL_S3` or `AWS_ENDPOINT_URL` is set (`platform/aws/provider/aws.go`, `stateBackendURL`), with a unit test. It is a bug fix, not a harness accommodation: every other provider call already honours the variable.

## Corrections to the #831 findings

- **Every ocel route streams, not only the image optimizer.** The membrane writes the `http-integration-response` envelope itself and every Function URL is `RESPONSE_STREAM`, so floci's "plain handler behind a streaming URL works" observation does not apply to anything ocel deploys. This is the load-bearing red cell.
- **The strict stub flag does not fire for CloudFront types.** `AWS::CloudFront::KeyValueStore` still lands as `arn:aws:stub:::EdgeRoutes` with the stack `CREATE_COMPLETE` and no warning logged, flag or no flag.
- **API Gateway on floci is reachable by custom domain**, which #831 did not test: the `Host: hello-express.example.test` request was routed to the right REST API and stage. Only the integration behind it is broken.

## Not tested here

- The SDK-using `examples/express` (postgres + blob): its RDS half belongs to [#848](https://github.com/ocelhq/ocel/issues/848).
- Container compute: ocel emits none on aws today.
- Anything under Cloudflare as the edge: needs a zone, out of the floci question.

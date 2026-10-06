# ocelhq

## What this codebase does

Ocel builds and deploys applications into a customer's own cloud account.
The monorepo contains a Go CLI, runtime SDKs, an AWS provider, and Cloudflare edge workers; the hosted console lives in github.com/ocelhq/console.

- The `ocel` CLI builds projects, starts provider subprocesses, and invokes authenticated local Connect RPC services.
- A public Cloudflare worker routes application traffic to static assets, edge bundles, or IAM-protected AWS Function URLs.
- Runtime blob handlers expose presign, polling, and signed upload-completion callback operations in customer applications.
- SQS revalidation, DynamoDB stream publishing, and Durable Object alarms are the asynchronous ingress families present.

There are no agent-tool or MCP server ingresses in the production source reviewed.
The only webhook-like ingress found is the SDK's signed blob completion callback.

## Auth shape

- The CLI signs in to the hosted console with the OAuth device-authorization grant and attaches the bearer access token it receives.
- CLI credentials prefer the operating-system keyring and fall back to a user-only credentials file; API calls use bearer access tokens.
- CLI-to-provider and runtime Connect RPC channels use a random per-process bearer token checked with constant-time comparison.
- Cloudflare control workers use bearer secrets or stored secret hashes; edge-to-Lambda and edge-to-AWS requests use SigV4 credentials.

Browser-facing application authorization is intentionally delegated to the deployed application's handlers.
Blob presign authorization is an application-supplied uploader middleware.
Blob completion callbacks are authorized with an HMAC over the session and canonical file metadata.

## Threat model

- An unauthenticated Internet client can reach public application routes, edge routing, and application blob endpoints.
- Project configuration, build inputs, framework bundles, and provider packages execute with the deploying user's local or cloud authority.
- Compromise of provider session tokens, Cloudflare bootstrap secrets, ISR secrets, or edge AWS credentials can affect customer infrastructure.
- Queue, stream, alarm, cache, and upload events may be duplicated, reordered, replayed, partially processed, or delivered after expiry.

The primary trust transitions are edge-to-origin, CLI-to-provider subprocess, and cloud-event-to-callback.
Customer cloud credentials and generated resource connection values are high-impact data.

## Project-specific patterns to flag

- Trace `callbackBaseUrl`, forwarded host/protocol headers, external rewrites, and origin URLs for SSRF and host-header trust issues.
- Check every project, resource, upload, promotion, and deployment lookup for organization or owner scoping before mutation or disclosure.
- Review presigned-upload keys, declared sizes and MIME types, session tags, expiry, callback HMACs, and state-transition idempotency together.
- Audit dynamic imports, provider binary resolution, build commands, routing manifests, and worker loaders as intentional code-execution seams.
- Verify SQS group failure handling, DynamoDB sequence handling, cache watermarks, Durable Object alarms, and retry paths remain replay-safe.

Also flag secrets persisted in relational or state storage, authorization headers forwarded across origins, and control headers accepted from public requests.
Treat generated deployment manifests and stored routing records as security-sensitive inputs even when their producer is authenticated.

## Known false-positives

- The unauthenticated development resource RPC server binds only to an ephemeral loopback address; loss of loopback confinement is still material.
- Local Wrangler and object-store defaults are development scaffolding and must not be interpreted as production credentials.
- Application Function URLs are configured for AWS IAM and invoked through SigV4 even though user handlers do not repeat that check.
- Public blob callbacks are expected; authenticity comes from the canonical HMAC rather than a browser session or source IP.
- Dynamic imports, subprocess execution, and user handler loading are core deployment behavior, not automatically command-injection findings.

Generated protobuf bindings, generated worker declarations, tests, fixtures, examples, and build output were excluded from review context.

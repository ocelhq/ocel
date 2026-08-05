// SigV4-signing the worker's AWS calls, under the edge reader's credentials
// (the same IAM user whose key backs the direct ISR reads). There are two, and
// they sign differently and deliberately so: the Function-URL forwards it
// proxies (edgeOriginFetch), and the S3/DynamoDB calls the cache entrypoint
// makes on the edge's behalf (awsServiceFetch).
//
// The Lambdas behind an app are provisioned with AWS_IAM auth, so every origin
// call the worker makes must be signed. Nothing else the worker fetches — static
// assets, external rewrites — is signed: those go to arbitrary hosts, and
// attaching AWS credentials to them would be both wrong and a needless leak.
import { AwsClient } from "aws4fetch";

// lambdaRegion extracts the AWS region from a Function URL host of the form
// `<id>.lambda-url.<region>.on.aws`. aws4fetch cannot infer region+service from
// the `.on.aws` domain (its guesser only understands `*.amazonaws.com`), so the
// region is parsed here and passed explicitly. An unrecognised host yields
// undefined; the caller fails loudly on that rather than signing against a
// silently-wrong region.
export function lambdaRegion(host: string): string | undefined {
  const labels = host.split(".");
  const i = labels.indexOf("lambda-url");
  if (i < 0 || i + 1 >= labels.length) return undefined;
  return labels[i + 1];
}

// The SigV4 headers the signature produces. They are the only headers copied
// from the signed proxy onto the real forward, so the request AWS authorizes
// carries exactly the signed material and nothing else it might reject.
const SIGV4_HEADERS = [
  "authorization",
  "x-amz-date",
  "x-amz-content-sha256",
  "x-amz-security-token",
];

// edgeOriginFetch builds the signing fetch the worker forwards to its Lambdas
// with, or undefined when no edge credentials are bound — an unsigned worker
// then forwards plainly, which only reaches a Lambda that is still public.
// Region is resolved per request from the Function URL host, so one client
// serves every function in a deploy regardless of which region each sits in.
//
// Only `host` (plus the amz date/payload headers) is signed, never the
// forwarded request's own headers. aws4fetch would otherwise sign every header
// present, and the Workers runtime rewrites some of them (accept-encoding, for
// one) between signing and the request leaving the edge — which changes a signed
// value and 403s at the Function URL. AWS requires only `host` signed for IAM
// auth, so the app's headers ride along unsigned and any in-transit rewrite is
// harmless. The body is signed (its hash is covered by the signature), so it is
// read up front and re-sent verbatim.
export function edgeOriginFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
): typeof fetch | undefined {
  if (!accessKeyId || !secretAccessKey) return undefined;
  const client = new AwsClient({ accessKeyId, secretAccessKey, service: "lambda" });
  return (async (input, init) => {
    const request = new Request(input as RequestInfo, init);
    const host = new URL(request.url).host;
    const region = lambdaRegion(host);
    if (!region) {
      // A forward target that is not a Function URL cannot be signed against a
      // known region; signing it anyway would 403 opaquely. Fail loudly instead.
      throw new Error(`cannot sign request to non-Function-URL host: ${host}`);
    }

    const hasBody = request.method !== "GET" && request.method !== "HEAD";
    const body = hasBody ? await request.arrayBuffer() : undefined;

    // Sign a bare request (no forwarded headers) so SignedHeaders is just host
    // + the amz headers; the body is passed through init so its hash is signed.
    const signed = await client.sign(request.url, {
      method: request.method,
      body,
      aws: { region },
    });

    const headers = new Headers(request.headers);
    for (const name of SIGV4_HEADERS) {
      const value = signed.headers.get(name);
      if (value) headers.set(name, value);
    }

    return fetch(
      new Request(request.url, {
        method: request.method,
        headers,
        body,
        redirect: "manual",
      }),
    );
  }) as typeof fetch;
}

// sqsRegion extracts the AWS region from a queue URL of the form
// `https://sqs.<region>.amazonaws.com/<account>/<name>.fifo`. Undefined for
// anything else; the caller then builds no sender at all rather than signing
// against a guessed region, which would 403 every send opaquely.
export function sqsRegion(queueUrl: string): string | undefined {
  let host: string;
  try {
    host = new URL(queueUrl).host;
  } catch {
    return undefined;
  }
  const labels = host.split(".");
  return labels[0] === "sqs" && labels.length > 2 ? labels[1] : undefined;
}

// sqsFetch signs the edge's SendMessage calls, and is deliberately its own
// client rather than a third entry in the awsServiceFetch map below, for two
// reasons that are both about this call and not about SQS:
//
//   - Region. The queue is substrate-global and its URL names its own region
//     (cloud/edge/resolver.go states that contract: "the region is derived in
//     the worker from the URL's own host rather than bound separately"), while
//     the map is built from OCEL_AWS_REGION — a var that is not part of this
//     path's three-var gate, so folding them together would make an enqueue
//     path silently absent, or silently wrongly-regioned, on a substrate that
//     binds one and not the other.
//   - Retries. The map retries once because a cache read is worth one retry.
//     This send is not: the fallback to originBlocking IS the retry, and it is
//     strictly better than a second attempt inside the same one-second budget.
export function sqsFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  region: string | undefined,
): typeof fetch | undefined {
  if (!accessKeyId || !secretAccessKey || !region) return undefined;
  const client = new AwsClient({
    accessKeyId,
    secretAccessKey,
    region,
    service: "sqs",
    retries: 0,
  });
  return ((input, init) =>
    client.fetch(input as RequestInfo, init)) as typeof fetch;
}

// The AWS services the cache entrypoint addresses under the same edge
// credentials. Named rather than inferred: aws4fetch's guesser only reads
// `*.amazonaws.com` hosts, and a wrong guess is a 403 with nothing to point at.
export type AwsService = "s3" | "dynamodb";

// A signed call to one of those services.
//
// Signing here is the aws4fetch default — host plus every signable header the
// request carries — and not the host-only signature edgeOriginFetch uses. That
// narrowing exists because the forward path re-sends *someone else's* headers,
// which the Workers runtime may rewrite after signing. Nothing on this path is
// forwarded: the worker composes each request itself, so no header changes
// between signing and sending, and both services need more than host anyway —
// DynamoDB rejects a call whose `x-amz-target` is unsigned, and S3 a call with
// no payload hash. Carrying the narrowing over would break both.
export type AwsServiceFetch = (
  service: AwsService,
  url: string,
  init?: RequestInit,
) => Promise<Response>;

// A cache read or write is optional work on a request's critical path: failing
// one costs a miss or a re-render, so aws4fetch's default ladder of ten retries
// — which backs off into the tens of seconds — costs far more than it buys. One
// retry covers a single blip; anything worse is a miss.
const serviceRetries = 1;

// awsServiceFetch signs the entrypoint's storage calls with the edge reader's
// credentials, or is undefined when the substrate binds no credentials or no
// region — leaving the edge uncached rather than failing to boot.
export function awsServiceFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  region: string | undefined,
): AwsServiceFetch | undefined {
  if (!accessKeyId || !secretAccessKey || !region) return undefined;
  const options = { accessKeyId, secretAccessKey, region, retries: serviceRetries };
  const clients: Record<AwsService, AwsClient> = {
    s3: new AwsClient({ ...options, service: "s3" }),
    dynamodb: new AwsClient({ ...options, service: "dynamodb" }),
  };
  return (service, url, init) => clients[service].fetch(url, init);
}

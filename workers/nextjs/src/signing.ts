// SigV4-signing the worker's Function-URL forwards. The Lambdas behind an app
// are provisioned with AWS_IAM auth, so every origin call the worker makes must
// be signed with the edge reader's credentials (the same IAM user whose key
// already backs the direct ISR reads). Nothing else the worker fetches — static
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

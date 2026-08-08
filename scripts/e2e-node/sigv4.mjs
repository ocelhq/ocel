// Signs the harness's own requests to the deployed express app.
//
// Every Lambda Function URL this deploy pipeline provisions carries
// AuthorizationType: AWS_IAM (cloud/aws/deploy/function.go) — including a
// plain node app's, which (unlike Next's) has no Cloudflare worker in front
// of it doing that signing on a real user's behalf
// (cloud/edge/framework/registry.go registers a worker only for the "next"
// framework; an express app is "served straight from its function URL", per
// that file's own comment). So where scripts/e2e-next's assertions can `fetch`
// the deployment URL unsigned — the worker they land on signs the forward
// itself — this harness has to sign every request it sends, or every one of
// them 403s before the app ever sees it.
//
// aws4fetch is already a dependency elsewhere in this repo for exactly this
// (workers/nextjs/src/signing.ts signs the same kind of Function URL call,
// under the same IAM auth, from the edge instead of from here).
import { AwsClient } from "aws4fetch";

/**
 * lambdaRegion extracts the AWS region from a Function URL host of the form
 * `<id>.lambda-url.<region>.on.aws`. aws4fetch cannot infer region+service
 * from the `.on.aws` domain (its guesser only understands
 * `*.amazonaws.com`), so the region is parsed here and passed explicitly.
 * Undefined for an unrecognized host — the caller fails loudly on that rather
 * than signing against a silently-wrong region.
 */
export function lambdaRegion(host) {
  const labels = String(host ?? "").split(".");
  const i = labels.indexOf("lambda-url");
  if (i < 0 || i + 1 >= labels.length) return undefined;
  return labels[i + 1];
}

/**
 * sigv4Fetch builds a `fetch`-compatible function that SigV4-signs every
 * request against the region its own URL names, under the given credentials.
 * Unlike workers/nextjs/src/signing.ts's edgeOriginFetch, this signs the whole
 * request (aws4fetch's default) rather than host-only: nothing here forwards
 * someone else's already-built request through a runtime that might rewrite a
 * header after signing (the reason that narrowing exists there), so there is
 * no such hazard to narrow away.
 */
export function sigv4Fetch({ accessKeyId, secretAccessKey, sessionToken }) {
  if (!accessKeyId || !secretAccessKey) {
    throw new Error(
      "sigv4Fetch needs AWS credentials to sign requests to the express app's Function URL " +
        "(AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY) — the same ones the `aws` CLI calls already use",
    );
  }
  const client = new AwsClient({ accessKeyId, secretAccessKey, sessionToken, service: "lambda" });
  return async (input, init) => {
    const url = typeof input === "string" || input instanceof URL ? String(input) : input.url;
    const region = lambdaRegion(new URL(url).host);
    if (!region) {
      throw new Error(`cannot sign a request to a non-Function-URL host: ${url}`);
    }
    return client.fetch(url, { ...init, aws: { region } });
  };
}

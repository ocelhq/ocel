/** PROTOTYPE — every mode written out (ocelhq/ocel#399). */
import { defineConfig, defineConfigStrict } from "ocel/config";
import { cfEdge } from "ocel/edge";
import { cloudflareDns } from "ocel/dns";
import awsProvider from "@ocel/provider-aws";
import { route53 } from "@ocel/provider-aws/dns";

// native (default): CloudFront in front, Route 53 writes the records.
export const native = defineConfig({
  slug: "shop",
  provider: awsProvider({ region: "eu-west-1" }),
  dns: route53(),
  domains: { production: "www.shop.com", preview: "*.preview.shop.com" },
  allowDegraded: ["edge-middleware", "ppr-resume"],
});

// cloudflare: Workers in front, records in the Cloudflare zone.
export const cloudflare = defineConfig({
  slug: "shop",
  provider: awsProvider(),
  edge: cfEdge(),
  dns: cloudflareDns({ zone: "023e105f4ecef8ad9ca31a8372d0c353" }),
  domains: { production: ["www.shop.com", "shop.com"], preview: "*.preview.shop.com" },
});

// none: REST API Gateway in front, nothing writes DNS — `ocel domain add` prints the records.
export const none = defineConfig({
  slug: "shop",
  provider: awsProvider({
    certificates: { "www.shop.com": "arn:aws:acm:eu-west-1:123456789012:certificate/abc" },
  }),
  edge: false,
  domains: { production: "www.shop.com" },
  allowDegraded: ["edge-middleware", "edge-runtime", "ppr-resume", "edge-cache", "streaming"],
});

// What the type checker refuses.
export const refused = defineConfig({
  slug: "shop",
  // @ts-expect-error unknown need names refuse: the CLI would too, at parse.
  allowDegraded: ["image-optimization"],
  // @ts-expect-error `native` has no marker — omit `edge` instead.
  edge: { kind: "native", options: {} },
});

// What only the CLI refuses with the plain `defineConfig`: route53 + cfEdge.
export const cliRefuses = defineConfig({
  slug: "shop",
  edge: cfEdge(),
  dns: route53(),
});

// The strict variant catches it at typecheck.
export const strictRefuses = defineConfigStrict({
  slug: "shop",
  edge: cfEdge(),
  // @ts-expect-error a Worker route needs a proxied record in a Cloudflare zone.
  dns: route53(),
});

import { defineConfig, type Need } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cfEdge } from "ocel/edge";
import { route53 } from "@ocel/provider-aws/dns";
import awsProvider from "@ocel/provider-aws";

type Exactly<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;

export const needsAreTheFive: Exactly<
  Need,
  "edge-middleware" | "edge-runtime" | "ppr-resume" | "edge-cache" | "streaming"
> = true;

export const cloudflareEdge = defineConfig({
  slug: "test-app",
  edge: cfEdge(),
  dns: cloudflareDns({ zone: "acme.com" }),
});

export const noEdge = defineConfig({
  slug: "test-app",
  edge: false,
  dns: route53({ zone: "Z123456789ABCDEFGHIJK" }),
});

export const originEdge = defineConfig({ slug: "test-app" });

export const namedNative = defineConfig({
  slug: "test-app",
  // @ts-expect-error omit `edge` for the origin's own edge; there is no "native" spelling
  edge: { kind: "native" },
});

export const edgeFromAString = defineConfig({
  slug: "test-app",
  // @ts-expect-error cfEdge takes no options
  edge: cfEdge("nonsense"),
});

export const edgeWithAZone = defineConfig({
  slug: "test-app",
  // @ts-expect-error a zone belongs to dns, not to the edge
  edge: cfEdge({ zone: "acme.com" }),
});

export const edgeTurnedOn = defineConfig({
  slug: "test-app",
  // @ts-expect-error true is not an edge
  edge: true,
});

export const zoneAsDns = defineConfig({
  slug: "test-app",
  // @ts-expect-error dns is declared with a marker, not a bare zone name
  dns: "acme.com",
});

export const everyNeed = defineConfig({
  slug: "test-app",
  allowDegraded: [
    "edge-middleware",
    "edge-runtime",
    "ppr-resume",
    "edge-cache",
    "streaming",
  ],
});

export const unknownNeed = defineConfig({
  slug: "test-app",
  // @ts-expect-error isr is not one of the five needs
  allowDegraded: ["isr"],
});

export const withCertificates = defineConfig({
  slug: "test-app",
  provider: awsProvider({
    certificates: {
      "app.acme.com": "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
    },
  }),
});

export const certificatesAsList = awsProvider({
  // @ts-expect-error certificates map a hostname to an arn
  certificates: ["arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234"],
});

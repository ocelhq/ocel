import { defineConfig, type Need } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
import { route53 } from "@ocel/provider-aws/dns";
import { apiGateway, cloudfront } from "@ocel/provider-aws/edge";
import awsProvider from "@ocel/provider-aws";

type Exactly<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;

export const needsAreTheFive: Exactly<
  Need,
  "edge-middleware" | "edge-runtime" | "ppr-resume" | "edge-cache" | "streaming"
> = true;

export const cloudflareEdge = defineConfig({
  slug: "test-app",
  edge: cloudflare(),
  dns: cloudflareDns({ zone: "acme.com" }),
});

export const cloudfrontEdge = defineConfig({
  slug: "test-app",
  edge: cloudfront(),
  dns: route53({ zone: "Z123456789ABCDEFGHIJK" }),
});

export const apiGatewayEdge = defineConfig({
  slug: "test-app",
  edge: apiGateway(),
  dns: route53({ zone: "Z123456789ABCDEFGHIJK" }),
});

export const providerDefaultEdge = defineConfig({ slug: "test-app" });

export const namedByHand = defineConfig({
  slug: "test-app",
  // @ts-expect-error an edge comes from a factory, never a hand-written marker
  edge: { kind: "cloudflare" },
});

export const edgeFromAString = defineConfig({
  slug: "test-app",
  // @ts-expect-error cloudflare takes no options
  edge: cloudflare("nonsense"),
});

export const edgeWithAZone = defineConfig({
  slug: "test-app",
  // @ts-expect-error a zone belongs to dns, not to the edge
  edge: cloudflare({ zone: "acme.com" }),
});

export const edgeTurnedOn = defineConfig({
  slug: "test-app",
  // @ts-expect-error true is not an edge
  edge: true,
});

export const edgeTurnedOff = defineConfig({
  slug: "test-app",
  // @ts-expect-error there is no off; omit `edge` for the provider's default
  edge: false,
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

import { defineConfig, type Need } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
import { exec, infisical } from "ocel/env-source";
import awsProvider from "ocel/providers/aws";
import { route53 } from "ocel/providers/aws/dns";
import { apiGateway, cloudfront } from "ocel/providers/aws/edge";

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

export const keyedByHand = defineConfig({
  slug: "test-app",
  provider: { aws: { region: "us-east-1" } },
  edge: { cloudflare: {} },
  dns: { cloudflare: { zone: "acme.com" } },
});

export const namedAloneByHand = defineConfig({
  slug: "test-app",
  provider: "aws",
  edge: "cloudfront",
  dns: "route53",
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
  allowDegraded: ["edge-middleware", "edge-runtime", "ppr-resume", "edge-cache", "streaming"],
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

export const bindingToAPublishedRecord = defineConfig({
  slug: "test-app",
  bindings: { postgres: { analytics: "@warehouse" } },
});

export const bindingWithoutItsSigil = defineConfig({
  slug: "test-app",
  // @ts-expect-error a published record is written "@name"
  bindings: { postgres: { analytics: "warehouse" } },
});

export const everyTierFromItsSource = defineConfig({
  slug: "test-app",
  envSource: {
    production: infisical({
      project: "p-1",
      environment: "prod",
      auth: {
        universal: {
          clientId: { var: "INFISICAL_CLIENT_ID" },
          clientSecret: { var: "INFISICAL_CLIENT_SECRET" },
        },
      },
    }),
    preview: exec({ command: ["./scripts/preview-env.sh", "{folder}"], format: "json" }),
    dev: infisical({ project: "p-1", environment: "dev" }),
  },
});

export const tierDefaultsNamed = defineConfig({
  slug: "test-app",
  envSource: { production: "builtin", preview: "builtin", dev: "dotenv" },
});

export const dotenvDeployed = defineConfig({
  slug: "test-app",
  // @ts-expect-error a deployed tier has no .env file to read
  envSource: { production: "dotenv" },
});

export const builtinInDev = defineConfig({
  slug: "test-app",
  // @ts-expect-error ocel dev reads nothing from your account
  envSource: { dev: "builtin" },
});

export const infisicalWithoutAnEnvironment = infisical({
  project: "p-1",
  // @ts-expect-error an Infisical source names the environment it reads
  environmnt: "prod",
});

export const universalAuthAsPlainText = infisical({
  project: "p-1",
  environment: "prod",
  // @ts-expect-error a credential is a value ocel holds, never plain text in the config
  auth: { universal: { clientId: "id", clientSecret: "secret" } },
});

export const execWithoutAFormat = exec(
  // @ts-expect-error exec names what its command prints
  { command: ["./scripts/dev-env.sh"] },
);

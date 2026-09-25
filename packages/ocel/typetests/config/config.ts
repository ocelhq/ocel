import { defineConfig, type Need } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
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

export const bindingInlineByUrl = defineConfig({
  slug: "test-app",
  bindings: { postgres: { orders: { url: { $env: "ORDERS_DATABASE_URL" } } } },
});

export const bindingInlinePerTier = defineConfig({
  slug: "test-app",
  bindings: {
    postgres: {
      orders: {
        production: {
          host: "db.abcdefghijkl.supabase.co",
          database: "postgres",
          username: { $env: "SUPABASE_DB_USER" },
          password: { $env: "SUPABASE_DB_PASSWORD" },
          tls: { mode: "verify-full", ca: { $env: "SUPABASE_CA" } },
        },
      },
    },
  },
});

export const bindingPasswordAsText = defineConfig({
  slug: "test-app",
  bindings: {
    postgres: {
      // @ts-expect-error a secret takes an ocel variable, never text
      orders: { host: "db", database: "d", username: "u", password: "hunter2" },
    },
  },
});

export const bindingUrlAsText = defineConfig({
  slug: "test-app",
  // @ts-expect-error a url carries its password, so it is an ocel variable
  bindings: { postgres: { orders: { url: "postgres://u:p@db/d" } } },
});

export const bucketInline = defineConfig({
  slug: "test-app",
  bindings: {
    bucket: {
      uploads: {
        endpoint: "https://${CF_ACCOUNT_ID}.r2.cloudflarestorage.com",
        region: "auto",
        bucket: { $env: "UPLOADS_BUCKET" },
        accessKeyId: { $env: "R2_ACCESS_KEY_ID" },
        secretAccessKey: { $env: "R2_SECRET_ACCESS_KEY" },
        publicBaseUrl: "https://cdn.acme.com",
      },
    },
  },
});

export const bucketSecretAsText = defineConfig({
  slug: "test-app",
  bindings: {
    bucket: {
      // @ts-expect-error a secret key takes an ocel variable, never text
      uploads: {
        endpoint: "https://s3.example.com",
        region: "auto",
        bucket: "acme",
        accessKeyId: { $env: "K" },
        secretAccessKey: "hunter2",
      },
    },
  },
});

export const registryPasswordAsAPlaceholder = defineConfig({
  slug: "test-app",
  registry: { server: "ghcr.io/acme", password: "${REGISTRY_TOKEN}" },
});

export const registryPasswordAsABareName = defineConfig({
  slug: "test-app",
  // @ts-expect-error the password is written as the placeholder "${REGISTRY_TOKEN}"
  registry: { server: "ghcr.io/acme", password: "REGISTRY_TOKEN" },
});

declare const readWithBuildEnv: string;

export const registryPasswordFromBuildEnv = defineConfig({
  slug: "test-app",
  // @ts-expect-error a value read with buildEnv is the secret itself, never its placeholder
  registry: { server: "ghcr.io/acme", password: readWithBuildEnv },
});

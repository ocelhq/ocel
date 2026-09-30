import { defineConfig, type Need } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
import { exec, infisical } from "ocel/env-source";
import awsProvider from "ocel/providers/aws";
import { route53 } from "ocel/providers/aws/dns";
import { apiGateway, cloudfront } from "ocel/providers/aws/edge";
import { alb } from "ocel/providers/gcp/edge";

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

export const cloudflareThroughATunnel = defineConfig({
  slug: "test-app",
  edge: cloudflare({ tunnel: true }),
});

export const cloudfrontThroughATunnel = defineConfig({
  slug: "test-app",
  // @ts-expect-error tunnel is an option of the cloudflare edge alone
  edge: cloudfront({ tunnel: true }),
});

export const apiGatewayThroughATunnel = defineConfig({
  slug: "test-app",
  // @ts-expect-error tunnel is an option of the cloudflare edge alone
  edge: apiGateway({ tunnel: true }),
});

export const albThroughATunnel = defineConfig({
  slug: "test-app",
  // @ts-expect-error tunnel is an option of the cloudflare edge alone
  edge: alb({ tunnel: true }),
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
  // @ts-expect-error a url contains its password, so it is an ocel variable
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
      uploads: {
        endpoint: "https://s3.example.com",
        region: "auto",
        bucket: "acme",
        accessKeyId: { $env: "K" },
        // @ts-expect-error a secret key takes an ocel variable, never text
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

export const everyTierFromItsEnvSource = defineConfig({
  slug: "test-app",
  envSource: {
    production: infisical({
      project: "p-1",
      environment: "prod",
      auth: {
        universal: {
          clientId: { $env: "INFISICAL_CLIENT_ID" },
          clientSecret: { $env: "INFISICAL_CLIENT_SECRET" },
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
  // @ts-expect-error an Infisical env source names the environment it reads
  environmnt: "prod",
});

export const universalAuthAsPlainText = infisical({
  project: "p-1",
  environment: "prod",
  // @ts-expect-error a credential is an ocel variable, never plain text in the config
  auth: { universal: { clientId: "id", clientSecret: "secret" } },
});

export const identityAuth = infisical({
  project: "p-1",
  environment: "prod",
  auth: { identity: { identityId: "b7d0" } },
});

export const writingValues = infisical({
  project: "p-1",
  environment: "prod",
  auth: { identity: { identityId: "b7d0" } },
  write: "values",
});

export const writingAnUnknownPolicy = infisical({
  project: "p-1",
  environment: "prod",
  auth: { identity: { identityId: "b7d0" } },
  // @ts-expect-error ocel writes never, missing keys, or values, and never deletes
  write: "delete",
});

export const identityAuthNamingACloud = infisical({
  project: "p-1",
  environment: "prod",
  // @ts-expect-error identity auth signs in as whatever cloud the target runs on, never one the config names
  auth: { aws: { identityId: "b7d0" } },
});

export const execWithoutAFormat = exec(
  // @ts-expect-error exec names what its command prints
  { command: ["./scripts/dev-env.sh"] },
);

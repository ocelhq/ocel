import { backing, type Backing, type OriginDescriptor } from "./config.prototype";

export function aws(options: { region?: string } = {}): OriginDescriptor {
  return { ...backing("origin", "aws", options), package: "@ocel/provider-aws" };
}
export const awsEdge = {
  cloudfront: (): Backing<"edge"> => backing("edge", "cloudfront"),
  apiGateway: (): Backing<"edge"> => backing("edge", "api-gateway"),
};
export const route53 = (zone?: string): Backing<"dns"> => backing("dns", "route53", { zone });
export const s3 = (): Backing<"bucket"> => backing("bucket", "s3");
export const aurora = (o: { maxAcu?: number } = {}): Backing<"postgres"> => backing("postgres", "aurora", o);

export function cloudflareOrigin(options: { accountId?: string } = {}): OriginDescriptor {
  return { ...backing("origin", "cloudflare", options), package: "@ocel/provider-cloudflare" };
}
export const cloudflare = (): Backing<"edge"> => backing("edge", "cloudflare");
export const cloudflareDns = (zone?: string): Backing<"dns"> => backing("dns", "cloudflare", { zone });
export const r2 = (): Backing<"bucket"> => backing("bucket", "r2");

export function vps(options: { host: string; user?: string }): OriginDescriptor {
  return { ...backing("origin", "vps", options), package: "@ocel/provider-vps" };
}
export const caddy = (): Backing<"edge"> => backing("edge", "caddy");
export const minio = (): Backing<"bucket"> => backing("bucket", "minio");
export const postgresDocker = (): Backing<"postgres"> => backing("postgres", "postgres-docker");

export function gcp(options: { project: string; region?: string }): OriginDescriptor {
  return { ...backing("origin", "gcp", options), package: "@ocel/provider-gcp" };
}

export const neon = (o: { project?: string } = {}): Backing<"postgres"> => backing("postgres", "neon", o);
export const s3Keys = (o: { endpoint?: string } = {}): Backing<"bucket"> => backing("bucket", "s3-keys", o);

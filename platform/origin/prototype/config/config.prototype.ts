declare const role: unique symbol;

export type Role =
  | "origin"
  | "edge"
  | "dns"
  | "bucket"
  | "postgres";

export interface Backing<R extends Role> {
  readonly [role]: R;
  readonly kind: string;
  readonly options: unknown;
}

export interface OriginDescriptor extends Backing<"origin"> {
  readonly package: string;
}

export type ResourceRole = Exclude<Role, "origin" | "edge" | "dns">;

export type ResourceBackings = {
  readonly [R in ResourceRole]?: Backing<R>;
};

export interface OcelConfig {
  slug: string;
  origin: OriginDescriptor;
  edge?: Backing<"edge">;
  dns?: Backing<"dns">;
  resources?: ResourceBackings;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}

export function backing<R extends Role>(r: R, kind: string, options: unknown = {}): Backing<R> {
  return { [role]: r, kind, options } as Backing<R>;
}

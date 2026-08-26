import type { ProviderDescriptor } from "ocel/config";

/** A manually specified SSH destination. */
export interface VpsSshTarget {
  /** The hostname or address to reach the machine at. */
  host: string;
  /** The port sshd listens on. Omit it and ssh's own default stands. */
  port?: number;
  /** The account to log in as. Omit it and ssh resolves the user itself. */
  user?: string;
  /** The private key to authenticate with, as a path. */
  identityFile?: string;
}

/** Options for the VPS provider, authored inline in `ocel.config.ts`. */
export interface VpsProviderOptions {
  /** The machine to deploy onto: a `Host` alias from ssh_config, or the destination spelled out. */
  ssh: string | VpsSshTarget;
  /**
   * The public key the `ocel-deploy` login is to answer to, as a path from `/` or
   * from `~/`. Omit it and bootstrap mirrors the keys the login it bootstraps with
   * already answers to.
   */
  deployKey?: string;
}

/** Declares a VPS as the provider `ocel deploy` provisions into. */
export default function vpsProvider(
  options: VpsProviderOptions,
): ProviderDescriptor {
  return { package: "@ocel/provider-vps", options };
}

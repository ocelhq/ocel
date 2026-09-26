/**
 * The container a box runs for an ocel resource, as `docker run` takes it.
 */
export interface VpsContainerArgs {
  /** The name the container runs under, which is also the host an app dials. */
  name: string;
  /** The project network the container joins, and the only thing that reaches it. */
  network: string;
  /** The labels a teardown and a class destroy find the container by. */
  labels: Record<string, string>;
  /** Ports published on the box. A resource publishes none. */
  publish: string[];
  /** What is mounted into the container: its own volume, and nothing else. */
  mounts: string[];
  /**
   * The image to run, pinned by digest (`…@sha256:…`). Swap it for a build of
   * the same engine that includes what you need, such as pgvector.
   */
  image: string;
  /** Arguments handed to the image's entrypoint, such as `["-c", "max_connections=200"]`. */
  args: string[];
  /** Environment for the container. The names the engine's credentials ride under stay ocel's. */
  env: Record<string, string>;
  /** A memory ceiling, as `docker run --memory` takes it: `"2g"`. */
  memory: string;
  /** A CPU ceiling, as `docker run --cpus` takes it: `"1.5"`. */
  cpus: string;
  /** The size of `/dev/shm`, as `docker run --shm-size` takes it: `"256m"`. */
  shmSize: string;
}

/**
 * The volume a box keeps an ocel resource's data on, as `docker volume create` takes it.
 */
export interface VpsVolumeArgs {
  /** The volume's name, which a teardown removes it by. */
  name: string;
  /** The labels a class destroy finds the volume by. */
  labels: Record<string, string>;
  /** The volume driver. Defaults to the engine's own `local`. */
  driver: string;
  /** Options for the driver, such as a bind onto a disk mounted at `/mnt/pg`. */
  driverOpts: Record<string, string>;
}

/**
 * One key per thing the vps provider provisions on the box for an ocel resource.
 */
export interface VpsResourceArgs {
  postgres: {
    container: VpsContainerArgs;
    volume: VpsVolumeArgs;
  };
  bucket: {
    container: VpsContainerArgs;
    volume: VpsVolumeArgs;
  };
}

/** The ocel resource types the vps provider renders patchable resources for. */
export type VpsResourceType = keyof VpsResourceArgs;

type OwnedFieldNames = {
  [T in VpsResourceType]: {
    [K in keyof VpsResourceArgs[T]]: readonly (keyof VpsResourceArgs[T][K])[];
  };
};

/**
 * The fields ocel fills itself — the name an app dials, the network that is
 * the resource's whole boundary, the labels it is found and removed by, and
 * what is published and mounted. A patch naming one is refused where it is
 * written, and the deploy refuses it again by name.
 */
export const vpsOwnedFields = {
  postgres: {
    container: ["name", "network", "labels", "publish", "mounts"],
    volume: ["name", "labels"],
  },
  bucket: {
    container: ["name", "network", "labels", "publish", "mounts"],
    volume: ["name", "labels"],
  },
} as const satisfies OwnedFieldNames;

type VpsOwned = typeof vpsOwnedFields;

type OwnedNames<L> = L extends readonly (infer F)[] ? Extract<F, string> : never;

/**
 * What a rule may patch under `vps`: the box's own args with the fields ocel
 * owns removed. Every leaf is a written-down value; a binding output has
 * nothing on a box to resolve against.
 */
export type VpsSurfaces = {
  [T in VpsResourceType]: {
    [K in keyof VpsResourceArgs[T]]: Partial<
      Omit<VpsResourceArgs[T][K], OwnedNames<VpsOwned[T][K & keyof VpsOwned[T]]>>
    >;
  };
};

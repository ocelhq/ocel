import { createHash } from "node:crypto";
import { hop } from "./hop.js";
import {
  coordinateFor,
  idFor,
  replacesFor,
  requestFor,
  type Coordinate,
  type PublisherInputs,
  type SSTLinkable,
} from "./publisher.js";

export type { Grant, LinkRecord, SSTInclude } from "./record.js";
export type {
  Coordinate,
  Described,
  PublisherInputs,
  SSTLinkable,
} from "./publisher.js";

interface DynamicHost {
  dynamic: {
    Resource: new (
      provider: unknown,
      name: string,
      props: Record<string, unknown>,
      opts?: unknown,
    ) => unknown;
  };
}

interface PublisherState extends Coordinate {
  digest: string;
  published: string[];
  pruned: number;
}

/**
 * The dynamic provider behind {@link publish}.
 *
 * It holds no state of its own: what it stores is the coordinate it published
 * to plus a digest of the records, never the link properties themselves, so a
 * resource credential never lands in the state file in the clear.
 */
export const provider = {
  async create(inputs: PublisherInputs) {
    const request = requestFor(inputs);
    const response = hop("publish-links", request);
    return { id: idFor(request), outs: state(inputs, response) };
  },

  async diff(_id: string, olds: PublisherState, news: PublisherInputs) {
    const replaces = replacesFor(olds, news);
    return {
      changes: replaces.length > 0 || olds.digest !== digestOf(news),
      replaces,
      deleteBeforeReplace: replaces.length > 0,
    };
  },

  async update(_id: string, _olds: unknown, news: PublisherInputs) {
    const response = hop("publish-links", requestFor(news));
    return { outs: state(news, response) };
  },

  async delete(_id: string, props: Coordinate) {
    hop("prune-links", coordinateFor(props as PublisherInputs));
  },
};

function state(
  inputs: PublisherInputs,
  response: { published?: string[]; pruned?: number },
): PublisherState {
  return {
    project: inputs.project,
    class: inputs.class,
    instance: inputs.instance,
    environment: inputs.environment,
    region: inputs.region,
    digest: digestOf(inputs),
    published: response.published ?? [],
    pruned: response.pruned ?? 0,
  };
}

function digestOf(inputs: PublisherInputs): string {
  return createHash("sha256")
    .update(JSON.stringify(requestFor(inputs)))
    .digest("hex");
}

/**
 * Publishes the named resources as ocel links, as a side effect of this apply.
 *
 * One instance is one ocel coordinate: leave `environment` off and the links
 * bind class-wide, serving every preview including the ephemeral ones. The
 * apply fails outright if the account holds no ocel substrate, and destroying
 * the stack prunes everything this instance published — and only that, since
 * every record it writes is stamped with the instance that owns it.
 *
 * `name` names the instance unless `args.instance` overrides it.
 */
export function publish(
  name: string,
  args: PublisherInputs,
  opts?: unknown,
): unknown {
  const util = host();
  return new util.dynamic.Resource(provider, name, resolvable(name, args), opts);
}

function resolvable(
  name: string,
  args: PublisherInputs,
): Record<string, unknown> {
  const links: Record<string, unknown> = {};
  for (const [link, resource] of Object.entries(args.links)) {
    links[link] =
      typeof (resource as SSTLinkable).getSSTLink === "function"
        ? { urn: resource.urn, ...(resource as SSTLinkable).getSSTLink() }
        : resource;
  }
  return {
    project: args.project,
    class: args.class,
    instance: args.instance ?? name,
    environment: args.environment,
    region: args.region,
    links,
  };
}

function host(): DynamicHost {
  const util = (globalThis as { $util?: DynamicHost }).$util;
  if (!util?.dynamic?.Resource) {
    throw new Error(
      "@ocel/sst publishes from inside sst.config.ts, where SST provides $util; it found no $util here",
    );
  }
  return util;
}

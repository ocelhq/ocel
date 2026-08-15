import type { PublishRequest } from "./hop.js";
import {
  grantsFor,
  recordFor,
  type Grant,
  type Linkable,
  type SSTInclude,
} from "./record.js";

const tool = "sst";

const classWideMarker = "*";

const delimiter = "#";

/** An SST component, as SST already describes itself to its own link consumers. */
export interface SSTLinkable {
  urn: string;
  getSSTLink(): {
    properties: Record<string, unknown>;
    include?: SSTInclude[];
  };
}

/**
 * A resource described directly: what SST's own link describes once Pulumi has
 * resolved it, and the shape to write by hand for anything SST does not describe.
 */
export interface Described {
  urn: string;
  properties: Record<string, unknown>;
  include?: SSTInclude[];
  grants?: Grant[];
}

/**
 * The ocel coordinate one `publish` instance targets.
 *
 * `class` is explicit: nothing is reconciled between SST stages and ocel
 * environments, and a stage name is never read. `environment` names one preview
 * environment and belongs only to the preview class; leaving it off binds the
 * links class-wide, serving every preview including the ephemeral ones.
 *
 * `instance` distinguishes two publishers writing into the same project, and
 * defaults to the resource name `publish` is given. Records belong to the
 * instance that wrote them: it is the only one that may overwrite or prune them.
 */
export interface Coordinate {
  project: string;
  class: "production" | "preview";
  instance?: string;
  environment?: string;
  region?: string;
}

/** A coordinate and the resources published there, one link record per binding. */
export interface PublisherInputs extends Coordinate {
  links: Record<string, SSTLinkable | Described>;
}

export function requestFor(inputs: PublisherInputs): PublishRequest {
  const request = coordinateFor(inputs);
  request.records = Object.entries(inputs.links).map(([name, resource]) =>
    recordFor(linkableOf(name, resource)),
  );
  return request;
}

export function coordinateFor(inputs: Coordinate): PublishRequest {
  if (!inputs.project) {
    throw new Error(
      "an ocel project is required: it is the project whose apps consume these links, and it is never read from an SST stage or stack name",
    );
  }
  if (inputs.class !== "production" && inputs.class !== "preview") {
    throw new Error(
      `class ${JSON.stringify(inputs.class ?? null)} is neither "production" nor "preview": a publisher targets an ocel coordinate, never a stage or stack name`,
    );
  }
  if (inputs.environment === classWideMarker) {
    throw new Error(
      `${classWideMarker} is reserved: leave the environment off to publish to the whole class, which serves every preview including the ephemeral ones`,
    );
  }
  if (inputs.environment && inputs.class !== "preview") {
    throw new Error(
      `environment ${inputs.environment} is named alongside class ${inputs.class}: an ocel coordinate is a class and, in preview, one preview environment`,
    );
  }

  const request: PublishRequest = {
    project: inputs.project,
    publisher: publisherName(inputs.instance),
    class: inputs.class,
  };
  if (inputs.environment) {
    request.environment = inputs.environment;
  }
  if (inputs.region) {
    request.region = inputs.region;
  }
  return request;
}

export function publisherName(instance: string | undefined): string {
  if (!instance) {
    throw new Error(
      "a publisher instance is required: it is what makes the records this stack wrote the ones it may overwrite and prune",
    );
  }
  if (instance.includes(delimiter)) {
    throw new Error(
      `publisher instance ${instance} may not contain ${delimiter}, the character the ocel store addresses its rows with`,
    );
  }
  return `${tool}:${instance}`;
}

export function idFor(request: PublishRequest): string {
  const coordinate = [request.publisher, request.project, request.class];
  if (request.environment) {
    coordinate.push(request.environment);
  }
  return coordinate.join("/");
}

export function replacesFor(olds: Coordinate, news: PublisherInputs): string[] {
  const previous = {
    project: olds.project,
    class: olds.class,
    instance: olds.instance,
    environment: olds.environment,
    region: olds.region,
  };
  return Object.keys(previous).filter(
    (field) =>
      previous[field as keyof typeof previous] !==
      news[field as keyof typeof previous],
  );
}

function linkableOf(name: string, resource: SSTLinkable | Described): Linkable {
  const described =
    typeof (resource as SSTLinkable).getSSTLink === "function"
      ? (resource as SSTLinkable).getSSTLink()
      : (resource as Described);

  const linkable: Linkable = {
    name,
    urn: resource.urn,
    properties: described.properties,
  };
  const grants =
    (described as Described).grants ?? grantsFor(name, described.include);
  if (grants) {
    linkable.grants = grants;
  }
  return linkable;
}

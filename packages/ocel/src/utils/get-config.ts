import { fromJson } from "@bufbuild/protobuf";
import { readLive } from "../env/live.js";
import {
  LinkSchema,
  LinkType,
  type Link,
} from "../gen/proto/links/v1/links_pb.js";

/** The link types an app resolves; a custom record is read by transforms alone. */
export type LinkCase = Exclude<
  NonNullable<Link["properties"]["case"]>,
  "custom"
>;

export type LinkProperties<TCase extends LinkCase> = Extract<
  Link["properties"],
  { case: TCase }
>["value"];

const typeOfCase: {
  [TCase in NonNullable<Link["properties"]["case"]>]: LinkType;
} = {
  postgres: LinkType.POSTGRES,
  bucket: LinkType.BUCKET,
  custom: LinkType.CUSTOM,
};

/** The type a link's properties case declares; UNSPECIFIED when it carries none. */
export function linkTypeOf(link: Link): LinkType {
  return link.properties.case
    ? typeOfCase[link.properties.case]
    : LinkType.UNSPECIFIED;
}

/** The env key a link of the given type is delivered under. */
export function linkKey(name: string, type: LinkType): string {
  return `OCEL_RESOURCE_${LinkType[type]}_${name}`;
}

/**
 * Reads the link delivered for a resource and hands back its typed
 * properties. Throws when nothing was delivered, when the payload is not a
 * link record, or when the record is of another type than the one asked for.
 */
export function getConfig<TCase extends LinkCase>(
  name: string,
  kind: TCase,
): LinkProperties<TCase> {
  const type = typeOfCase[kind];
  const key = linkKey(name, type);
  const raw = readLive(key) ?? process.env[key];

  if (!raw) {
    throw new Error(
      `Value for ${key} is not defined. Run \`ocel dev\` to resolve it locally, or \`ocel deploy\` to have it delivered from the resource this app links.`,
    );
  }

  let link: Link;
  try {
    link = fromJson(LinkSchema, JSON.parse(raw));
  } catch (cause) {
    throw new Error(
      `${key} does not carry a link record, so this app cannot read it as a ${LinkType[type]}`,
      { cause },
    );
  }

  if (link.properties.case !== kind) {
    throw new Error(
      `${key} carries a ${LinkType[linkTypeOf(link)]} link, and this app reads it as a ${LinkType[type]}`,
    );
  }
  return link.properties.value as LinkProperties<TCase>;
}

export const RUNTIME_ADDRESS = "OCEL_RUNTIME_ADDRESS";

export const SESSION_TOKEN = "OCEL_SESSION_TOKEN";

export const getRuntimeAddress = () => {
  const address = process.env[RUNTIME_ADDRESS];

  if (!address) {
    throw new Error(
      `${RUNTIME_ADDRESS} is not defined, so no resource the ocel runtime serves can be reached. Run \`ocel dev\` to serve it locally, or \`ocel deploy\` to have the deployed runtime's address delivered.`,
    );
  }

  return address;
};

export const getSessionToken = () => process.env[SESSION_TOKEN];

import { fromJson } from "@bufbuild/protobuf";
import { readLiveFile } from "../env/file.js";
import { readLive } from "../env/live.js";
import { type Binding, BindingSchema } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { MissingBindingError } from "./errors.js";
import { isDiscovering, unprovisioned } from "./unprovisioned.js";

export type BindingCase = Exclude<NonNullable<Binding["properties"]["case"]>, "custom">;

export type BindingProperties<TCase extends BindingCase> = Extract<
  Binding["properties"],
  { case: TCase }
>["value"];

export function bindingKey(name: string, kind: BindingCase): string {
  return `OCEL_RESOURCE_${kind.toUpperCase()}_${name}`;
}

export function getConfig<TCase extends BindingCase>(
  name: string,
  kind: TCase,
): BindingProperties<TCase> {
  const key = bindingKey(name, kind);
  const type = kind.toUpperCase();
  const raw = readLive(key) ?? process.env[key] ?? readLiveFile(key);

  if (!raw) {
    throw new MissingBindingError(key);
  }

  let binding: Binding;
  try {
    binding = fromJson(BindingSchema, JSON.parse(raw));
  } catch (cause) {
    throw new Error(
      `${key} does not contain a binding record, so this app cannot read it as a ${type}`,
      { cause },
    );
  }

  if (binding.properties.case !== kind) {
    throw new Error(
      `${key} contains a ${binding.properties.case?.toUpperCase() ?? "UNSPECIFIED"} binding, and this app reads it as a ${type}`,
    );
  }
  return binding.properties.value as BindingProperties<TCase>;
}

export function lazyBinding<TCase extends BindingCase, T>(
  what: string,
  name: string,
  kind: TCase,
  open: (properties: BindingProperties<TCase>) => T,
): (access: string) => T {
  let opened: { value: T } | undefined;
  return (access) => {
    if (isDiscovering()) {
      throw unprovisioned(what, access);
    }
    opened ??= { value: open(getConfig(name, kind)) };
    return opened.value;
  };
}

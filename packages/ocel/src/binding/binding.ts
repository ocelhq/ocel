import { fromJson } from "@bufbuild/protobuf";
import { readLiveFile } from "../env/file.js";
import { readLive } from "../env/live.js";
import {
  type Binding,
  BindingSchema,
  BindingType,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";

/** The binding types an app resolves; a custom record is read by transforms alone. */
export type BindingCase = Exclude<NonNullable<Binding["properties"]["case"]>, "custom">;

/** The properties a binding of the given type carries. */
export type BindingProperties<TCase extends BindingCase> = Extract<
  Binding["properties"],
  { case: TCase }
>["value"];

const typeOfCase: {
  [TCase in NonNullable<Binding["properties"]["case"]>]: BindingType;
} = {
  postgres: BindingType.POSTGRES,
  bucket: BindingType.BUCKET,
  custom: BindingType.CUSTOM,
  topic: BindingType.TOPIC,
  task: BindingType.TASK,
  kv: BindingType.KV,
  realtime: BindingType.REALTIME,
};

/** The type a binding's properties case declares; UNSPECIFIED when it has none. */
export function bindingTypeOf(binding: Binding): BindingType {
  return binding.properties.case ? typeOfCase[binding.properties.case] : BindingType.UNSPECIFIED;
}

/** The env key a binding of the given type is delivered under. */
export function bindingKey(name: string, type: BindingType): string {
  return `OCEL_RESOURCE_${BindingType[type]}_${name}`;
}

/**
 * Reads the binding delivered for a resource and hands back its typed
 * properties. Throws when nothing was delivered, when the payload is not a
 * binding record, or when the record is of another type than the one asked for.
 */
export function getConfig<TCase extends BindingCase>(
  name: string,
  kind: TCase,
): BindingProperties<TCase> {
  const found = findBinding(name, kind);
  if (found instanceof Error) {
    throw found;
  }
  return found.properties.value as BindingProperties<TCase>;
}

/**
 * The error for a resource whose binding of the given type was not delivered,
 * is not a binding record, or is of another type; undefined when it was delivered.
 */
export function refuseUnbound(name: string, kind: BindingCase): Error | undefined {
  const found = findBinding(name, kind);
  return found instanceof Error ? found : undefined;
}

function findBinding(name: string, kind: BindingCase): Binding | Error {
  const type = typeOfCase[kind];
  const key = bindingKey(name, type);
  const raw = readLive(key) ?? readLiveFile(key) ?? process.env[key];

  if (!raw) {
    return new Error(
      `${key} is not delivered to this process: \`ocel dev\` delivers it locally and \`ocel deploy\` to the deployed app, but a build gets no bindings, so code that runs while building, such as prerendering a page, cannot use the resource`,
    );
  }

  let binding: Binding;
  try {
    binding = fromJson(BindingSchema, JSON.parse(raw));
  } catch (cause) {
    return new Error(
      `${key} does not contain a binding record, so this app cannot read it as a ${BindingType[type]}`,
      { cause },
    );
  }

  if (binding.properties.case !== kind) {
    return new Error(
      `${key} contains a ${BindingType[bindingTypeOf(binding)]} binding, and this app reads it as a ${BindingType[type]}`,
    );
  }
  return binding;
}

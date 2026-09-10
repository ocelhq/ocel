import { fromJson } from "@bufbuild/protobuf";
import { readLive } from "../env/live.js";
import {
  type Binding,
  BindingSchema,
  BindingType,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";

/** The binding types an app resolves; a custom record is read by transforms alone. */
export type BindingCase = Exclude<NonNullable<Binding["properties"]["case"]>, "custom">;

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
};

/** The type a binding's properties case declares; UNSPECIFIED when it carries none. */
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
  const type = typeOfCase[kind];
  const key = bindingKey(name, type);
  const raw = readLive(key) ?? process.env[key];

  if (!raw) {
    throw new Error(
      `Value for ${key} is not defined. Run \`ocel dev\` to resolve it locally, or \`ocel deploy\` to have it delivered from the resource this app bindings.`,
    );
  }

  let binding: Binding;
  try {
    binding = fromJson(BindingSchema, JSON.parse(raw));
  } catch (cause) {
    throw new Error(
      `${key} does not carry a binding record, so this app cannot read it as a ${BindingType[type]}`,
      { cause },
    );
  }

  if (binding.properties.case !== kind) {
    throw new Error(
      `${key} carries a ${BindingType[bindingTypeOf(binding)]} binding, and this app reads it as a ${BindingType[type]}`,
    );
  }
  return binding.properties.value as BindingProperties<TCase>;
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

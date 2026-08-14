import { z } from "zod";

export const TOKEN_NAMESPACE = "ocel:";

export const shapes = {
  "ocel:postgres": z.object({
    host: z.string(),
    port: z.string(),
    database: z.string(),
    username: z.string(),
    password: z.string(),
  }),
  "ocel:bucket": z.object({
    bucket: z.string(),
  }),
} as const;

export type OwnedToken = keyof typeof shapes;

export type OwnedProperties<TToken extends OwnedToken> = z.infer<
  (typeof shapes)[TToken]
>;

export function shapeOf(type: string) {
  return Object.hasOwn(shapes, type)
    ? shapes[type as OwnedToken]
    : undefined;
}

import type { Grant } from "./grants.js";

/**
 * One `provider.variablestore.v1.PublishedBinding`, ready for protobuf JSON: the
 * binding an app reads, and beside it the grants a deploy gives the apps bound to
 * it. An app is handed the binding alone.
 */
export interface PublishedBinding<B> {
  binding: B;
  grants?: Grant[];
}

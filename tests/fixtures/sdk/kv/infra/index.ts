import { kv } from "ocel/kv";

export const cache = kv("cache", {
  memory: "64mb",
  entries: {
    entry: kv.text("journey/:key"),
  },
});

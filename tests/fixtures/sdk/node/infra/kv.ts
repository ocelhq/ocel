import { kv } from "ocel/kv";
import { z } from "zod";

const Profile = z.object({
  name: z.string(),
  visits: z.number().int(),
  tags: z.array(z.string()),
});

export const cache = kv("cache", {
  memory: "64mb",
  entries: {
    text: kv.text("text/:key"),
    counter: kv.counter("counter/:key"),
    json: kv.json("json/:key", { schema: Profile }),
    lenient: kv.json("lenient/:key", { schema: Profile, onInvalid: "miss" }),
    list: kv.list("list/:key"),
    set: kv.set("set/:key"),
    fleeting: kv.text("fleeting/:key", { ttl: "2s" }),
    docs: kv.text("docs/:id"),
    meta: kv.text("docs/:id/meta"),
  },
});

export const bounded = kv("bounded", { memory: "32mb", eviction: "noeviction" });

export const evicting = kv("evicting", { memory: "32mb", eviction: "allkeys-lru" });

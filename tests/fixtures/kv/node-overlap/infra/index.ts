import { kv } from "ocel/kv";

export const notes = kv("notes", {
  entries: {
    byId: kv.text("notes/:id"),
    latest: kv.text(":kind/latest"),
  },
});

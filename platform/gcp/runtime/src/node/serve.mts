import { setTagRecordWriter } from "@framework/node-runtime/tag-records";
import { openPurgeFor } from "../cache/purge-target.mjs";
import { newTagRecordWriter } from "./tag-records.mjs";

setTagRecordWriter(newTagRecordWriter(openPurgeFor(process.env)));

await import("@framework/node-runtime/serve");

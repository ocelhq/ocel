import { setTagRecordWriter } from "@framework/node-runtime/tag-records";
import { openPurgeFor } from "../next/purge-target.mjs";
import { newGcpTagRecordWriter } from "./tag-records.mjs";

setTagRecordWriter(newGcpTagRecordWriter(openPurgeFor(process.env)));

await import("@framework/node-runtime/serve");

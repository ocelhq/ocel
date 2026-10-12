import http from "node:http";
import { setTagRecordWriter } from "@framework/node-runtime/tag-records";
import { openPurgeFor } from "../cache/purge-target.mjs";
import { withholdOverTaggedResponsesFromCloudCdn } from "./cloud-cdn.mjs";
import { newTagRecordWriter } from "./tag-records.mjs";

withholdOverTaggedResponsesFromCloudCdn(http.ServerResponse.prototype);
setTagRecordWriter(newTagRecordWriter(openPurgeFor(process.env)));

await import("@framework/node-runtime/serve");

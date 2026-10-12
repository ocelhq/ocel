import { boot } from "@framework/node-runtime/boot";
import { installCompileCacheFlush, installCompileCacheWarm } from "@framework/node-runtime/host";
import { setTagRecordWriter } from "@framework/node-runtime/tag-records";
import { newAwsTagRecordWriter } from "./tag-records.mjs";

boot({
  hooks: () => {
    installCompileCacheFlush();
    installCompileCacheWarm(undefined);
    setTagRecordWriter(newAwsTagRecordWriter(process.env));
  },
});

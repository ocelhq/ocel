import { boot } from "./boot.mjs";
import { installCompileCacheFlush, installCompileCacheWarm } from "./membrane.mjs";

boot({
  hooks: () => {
    installCompileCacheFlush();
    installCompileCacheWarm(undefined);
  },
});

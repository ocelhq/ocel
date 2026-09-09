import { boot } from "./boot.mjs";
import { installCompileCacheFlush, installCompileCacheWarm } from "./host.mjs";

boot({
  hooks: () => {
    installCompileCacheFlush();
    installCompileCacheWarm(undefined);
  },
});

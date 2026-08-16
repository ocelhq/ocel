/// <reference path="./.sst/platform/config.d.ts" />

const region = "us-east-1";

export default $config({
  app() {
    return {
      name: "with-sst",
      home: "aws",
      providers: { aws: { region } },
    };
  },
  async run() {
    // FIXME: @ocel/sst shapes link records and stops there. Publishing `orders` as an
    // ocel link waits on the successor ticket re-plumbing the adapter onto `ocel link`,
    // which rewrites this run block.
    throw new Error(
      "with-sst publishes nothing: @ocel/sst has not been re-plumbed onto `ocel link`, so no ocel link record reaches the store and the app consuming `orders` would resolve nothing",
    );
  },
});

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
    const { link } = await import("@ocel/sst");
    const vpc = new sst.aws.Vpc("Vpc");
    const orders = new sst.aws.Postgres("Orders", { vpc });

    link.postgres("orders", orders);

    return { host: orders.host, database: orders.database, port: orders.port };
  },
});

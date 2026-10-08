const crypto = require("node:crypto");
const fs = require("node:fs");

const dir = process.env.OCEL_LIVE_DIR;
const value = fs.readFileSync(`${dir}/OCEL_RESOURCE_POSTGRES_my-db`, "utf8");
const port = /:(\d+)\/db$/.exec(value)[1];

fs.mkdirSync("dist", { recursive: true });
fs.writeFileSync("dist/proof", crypto.createHash("sha256").update(value).digest("hex"));
fs.writeFileSync("dist/session-token", crypto.createHash("sha256").update(process.env.OCEL_SESSION_TOKEN ?? "").digest("hex"));
fs.writeFileSync("dist/in-environment", String(process.env["OCEL_RESOURCE_POSTGRES_my-db"] !== undefined));

fetch(`http://127.0.0.1:${port}/`)
  .then((response) => response.text())
  .then((body) => fs.writeFileSync("dist/reached", body));

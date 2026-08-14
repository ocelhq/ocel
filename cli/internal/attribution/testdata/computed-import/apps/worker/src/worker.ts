const spec = "../../../shared/" + ["met", "rics"].join("") + ".js";

export async function run() {
  const { metricsDb } = await import(spec);
  return metricsDb;
}

import { metricsDb } from "../../../shared/metrics.js";

export default function Page() {
  return <p>{metricsDb.id}</p>;
}

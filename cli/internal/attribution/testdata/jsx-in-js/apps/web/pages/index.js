import { metricsDb } from "../../../shared/metrics.js";
import { trace } from "telemetry";

export default function Page() {
  trace();
  return <p>{metricsDb.id}</p>;
}

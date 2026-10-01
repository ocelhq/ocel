import { createRealtimeClient } from "../realtime/client.js";
import type { rt } from "./realtime-server.js";

const live = createRealtimeClient<typeof rt>({ url: "/api/realtime" });
live.subscribe("orders/:orderId", { params: { orderId: "o-1" } }, (event) => console.log(event));
void live.publish("orders/:orderId", { params: { orderId: "o-1" }, body: { status: "paid" } });

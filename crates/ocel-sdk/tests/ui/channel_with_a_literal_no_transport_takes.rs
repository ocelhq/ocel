#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders_x", event = Event, public)]
struct Orders;

fn main() {}

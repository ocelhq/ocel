#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders/:order_id", event = Event)]
struct Orders {
    user: String,
}

fn main() {}

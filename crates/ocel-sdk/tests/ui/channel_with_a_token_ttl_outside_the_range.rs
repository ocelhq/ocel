#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Event, public, token_ttl = "301s")]
struct Status;

fn main() {}

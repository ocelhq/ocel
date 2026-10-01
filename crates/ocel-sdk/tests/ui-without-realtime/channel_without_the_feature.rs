#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Event, public)]
struct Status;

fn main() {}

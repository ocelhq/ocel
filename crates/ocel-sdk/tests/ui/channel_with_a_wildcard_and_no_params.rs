#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Event, wildcard, public)]
struct Status;

fn main() {}

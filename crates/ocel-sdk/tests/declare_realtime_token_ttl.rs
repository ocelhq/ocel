#![cfg(feature = "realtime")]

#[allow(dead_code)]
#[derive(serde::Serialize, serde::Deserialize)]
struct Event;

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Event, public, token_ttl = "30s")]
struct Status;

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "news", event = Event, public, token_ttl = "45s")]
struct News;

#[test]
fn channels_of_one_resource_declaring_different_token_ttls_are_refused() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let discovered = ocel::discover().expect_err("two token TTLs for one resource");
    let built = ocel::realtime::Realtime::builder("app")
        .build()
        .err()
        .expect("two token TTLs for one resource");

    assert!(matches!(discovered, ocel::Error::Definition { .. }));
    for refused in [discovered.to_string(), built.to_string()] {
        for want in [
            "30s",
            "45s",
            "declare_realtime_token_ttl.rs:10",
            "declare_realtime_token_ttl.rs:15",
            "one token TTL",
        ] {
            assert!(
                refused.contains(want),
                "error = {refused}, want {want} in it"
            );
        }
    }
}

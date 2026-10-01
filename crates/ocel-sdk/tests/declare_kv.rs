mod collector;

use collector::{collector, Received, DECLARE};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::app::resources::v1::{KvShape, ResourceType};

#[allow(dead_code)]
#[derive(serde::Serialize, serde::Deserialize)]
struct Session {
    user: String,
}

#[allow(dead_code)]
#[derive(ocel::KvKey)]
#[ocel(pattern = "requests/:user_id", counter, ttl = "10s")]
struct Requests {
    user_id: String,
}

#[allow(dead_code)]
#[derive(ocel::KvKey)]
#[ocel(name = "session", pattern = "session/:id", json = Session, ttl = "30d")]
struct SessionKey {
    id: String,
}

#[allow(dead_code)]
#[derive(ocel::KvKey)]
#[ocel(pattern = "greeting", text)]
struct Greeting;

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Infra {
    #[ocel(
        version = "8",
        eviction = "allkeys-lru",
        memory = "256mb",
        entries = [Requests, SessionKey, Greeting]
    )]
    cache: ocel::Kv,
    plain: ocel::Kv,
}

#[test]
fn a_kv_field_declares_its_store_with_every_entry_and_the_line_each_was_declared_on() {
    let (url, requests) = collector(2);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let declares: Vec<_> = (0..2)
        .map(|_| requests.recv().expect("a request"))
        .filter(|one| one.path == DECLARE)
        .map(|one| Received::declare(&one))
        .collect();
    assert_eq!(declares.len(), 2);
    let cache = &declares[0];
    assert_eq!(cache.resource.r#type, ResourceType::RESOURCE_TYPE_KV);
    assert_eq!(cache.resource.name, "cache");
    assert!(
        cache.source.ends_with("declare_kv.rs:41"),
        "source = {}",
        cache.source
    );
    let Some(Config::Kv(config)) = cache.config.as_ref() else {
        panic!("the store declared {:?}, want a kv config", cache.config);
    };
    assert_eq!(
        (
            config.version.as_str(),
            config.eviction.as_str(),
            config.memory.as_str()
        ),
        ("8", "allkeys-lru", "256mb")
    );
    let entries: Vec<_> = config
        .entries
        .iter()
        .map(|entry| {
            let line = entry
                .source
                .rsplit(':')
                .next()
                .unwrap_or_default()
                .to_string();
            (
                entry.name.as_str(),
                entry.pattern.as_str(),
                entry.shape,
                line,
            )
        })
        .collect();
    assert_eq!(
        entries,
        [
            (
                "requests",
                "requests/:user_id",
                KvShape::KV_SHAPE_COUNTER.into(),
                "16".to_string()
            ),
            (
                "session",
                "session/:id",
                KvShape::KV_SHAPE_JSON.into(),
                "23".to_string()
            ),
            (
                "greeting",
                "greeting",
                KvShape::KV_SHAPE_TEXT.into(),
                "30".to_string()
            ),
        ]
    );

    let Some(Config::Kv(plain)) = declares[1].config.as_ref() else {
        panic!("the plain store declared no kv config");
    };
    assert_eq!(
        (
            plain.version.as_str(),
            plain.eviction.as_str(),
            plain.memory.as_str(),
            plain.entries.len()
        ),
        ("", "", "", 0)
    );
}

#[test]
fn every_accessor_is_refused_during_discovery() {
    std::env::set_var("OCEL_PHASE", "discovery");
    let infra = Infra {
        cache: ocel::Kv::new("cache"),
        plain: ocel::Kv::new("plain"),
    };

    let err = infra.cache.connection_string().expect_err("unprovisioned");
    assert!(matches!(err, ocel::Error::Unprovisioned { .. }), "{err}");
}

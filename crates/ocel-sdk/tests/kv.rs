#![cfg(feature = "kv")]

mod valkey;

use ocel::{Error, Kv};
use std::collections::HashSet;
use std::time::Duration;
use valkey::Valkey;

#[derive(Debug, PartialEq, serde::Serialize, serde::Deserialize)]
struct Session {
    user: String,
    visits: u32,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "greeting", text)]
struct Greeting;

#[derive(ocel::KvKey)]
#[ocel(pattern = "notes/:id", text)]
struct Note {
    id: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "hits/:page", counter)]
struct Hits {
    page: &'static str,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", json = Session)]
struct SessionKey {
    id: u64,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "lenient/:id", json = Session, on_invalid = "miss")]
struct Lenient {
    id: u64,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "recent/:user", list)]
struct Recent {
    user: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "online/:room", set)]
struct Online {
    room: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "ttl/session/:id", text, ttl = "30d")]
struct ExpiringSession {
    id: u32,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "ttl/hits/:id", counter, ttl = "10s")]
struct ExpiringHits {
    id: u32,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "ttl/recent/:id", list, ttl = "1h")]
struct ExpiringRecent {
    id: u32,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "ttl/online/:id", set, ttl = "5m")]
struct ExpiringOnline {
    id: u32,
}

#[derive(ocel::Resources)]
struct Listed {
    #[ocel(entries = [Greeting, Hits])]
    listed: Kv,
}

#[tokio::test]
async fn a_key_whose_entry_the_store_does_not_list_is_refused() {
    let server = Valkey::start();
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var("OCEL_RESOURCE_KV_listed", server.binding("listed"));
    let cache = Listed::load().unwrap().listed;
    let unlisted = || Note { id: "a".into() };

    cache.entry(Greeting).set("hello").await.unwrap();
    assert_eq!(
        cache.get_many(&[Hits { page: "home" }]).await.unwrap(),
        [None]
    );
    for err in [
        cache.entry(unlisted()).set("x").await.unwrap_err(),
        cache.entry(unlisted()).get().await.unwrap_err(),
        cache.entry(unlisted()).delete().await.unwrap_err(),
        cache.get_many(&[unlisted()]).await.unwrap_err(),
    ] {
        assert!(
            matches!(err, Error::UndeclaredKvEntry { ref store, ref key } if store == "listed" && key.ends_with("Note")),
            "{err}"
        );
    }
    assert_eq!(server.text("notes/a"), None);
}

fn store(name: &str) -> Kv {
    Kv::new(name)
        .with_entry::<Greeting>()
        .with_entry::<Note>()
        .with_entry::<Hits>()
        .with_entry::<SessionKey>()
        .with_entry::<Lenient>()
        .with_entry::<Recent>()
        .with_entry::<Online>()
        .with_entry::<ExpiringSession>()
        .with_entry::<ExpiringHits>()
        .with_entry::<ExpiringRecent>()
        .with_entry::<ExpiringOnline>()
}

fn serve(name: &str) -> (Kv, Valkey) {
    let server = Valkey::start();
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(format!("OCEL_RESOURCE_KV_{name}"), server.binding(name));
    (store(name), server)
}

#[tokio::test]
async fn a_text_entry_reads_what_was_written_and_misses_as_none() {
    let (cache, server) = serve("text");

    assert_eq!(cache.entry(Greeting).get().await.unwrap(), None);
    cache.entry(Greeting).set("hello").await.unwrap();
    cache
        .entry(Note { id: "a".into() })
        .set("first")
        .await
        .unwrap();
    assert_eq!(
        cache.entry(Greeting).get().await.unwrap().as_deref(),
        Some("hello")
    );
    assert_eq!(server.text("greeting").as_deref(), Some("hello"));
    assert_eq!(
        cache
            .get_many(&[Note { id: "a".into() }, Note { id: "b".into() }])
            .await
            .unwrap(),
        [Some("first".to_string()), None]
    );
    assert!(cache.entry(Note { id: "a".into() }).delete().await.unwrap());
    assert!(!cache.entry(Note { id: "a".into() }).delete().await.unwrap());
}

async fn read_whole<K>(cache: &Kv, key: K) -> Option<<K::Shape as ocel::kv::Readable>::Value>
where
    K: ocel::KvKey,
    K::Shape: ocel::kv::Readable,
{
    cache.entry(key).get().await.unwrap()
}

#[tokio::test]
async fn a_read_generic_over_its_key_is_bounded_by_the_public_readable_trait() {
    let (cache, _server) = serve("generic");

    cache.entry(Greeting).set("hello").await.unwrap();
    cache.entry(Hits { page: "home" }).set(3).await.unwrap();
    assert_eq!(read_whole(&cache, Greeting).await.as_deref(), Some("hello"));
    assert_eq!(read_whole(&cache, Hits { page: "home" }).await, Some(3));
}

#[tokio::test]
async fn a_counter_counts_atomically_from_zero() {
    let (cache, _server) = serve("counter");
    let home = || cache.entry(Hits { page: "home" });

    assert_eq!(home().get().await.unwrap(), None);
    assert_eq!(home().increment(1).await.unwrap(), 1);
    assert_eq!(home().increment(5).await.unwrap(), 6);
    assert_eq!(home().decrement(2).await.unwrap(), 4);
    assert_eq!(
        cache
            .entry(Hits { page: "other" })
            .decrement(1)
            .await
            .unwrap(),
        -1
    );
    cache.entry(Hits { page: "set" }).set(10).await.unwrap();
    assert_eq!(
        cache
            .get_many(&[
                Hits { page: "home" },
                Hits { page: "set" },
                Hits { page: "none" }
            ])
            .await
            .unwrap(),
        [Some(4), Some(10), None]
    );
}

#[tokio::test]
async fn a_counter_holding_no_integer_is_invalid() {
    let (cache, server) = serve("badcounter");
    server.set_text("hits/home", "lots");

    let err = cache
        .entry(Hits { page: "home" })
        .get()
        .await
        .expect_err("no integer");
    assert!(
        matches!(err, Error::InvalidKvValue { ref key, .. } if key == "hits/home"),
        "{err}"
    );
}

#[tokio::test]
async fn a_json_entry_decodes_into_its_type_and_refuses_what_does_not() {
    let (cache, server) = serve("json");
    let session = Session {
        user: "ada".into(),
        visits: 2,
    };

    cache
        .entry(SessionKey { id: 1 })
        .set(&session)
        .await
        .unwrap();
    assert_eq!(
        cache.entry(SessionKey { id: 1 }).get().await.unwrap(),
        Some(session)
    );
    assert_eq!(cache.entry(SessionKey { id: 9 }).get().await.unwrap(), None);

    for (id, stored) in [(3, r#"{"user":7}"#), (4, "not json")] {
        server.set_text(&format!("session/{id}"), stored);
        server.set_text(&format!("lenient/{id}"), stored);
        let err = cache
            .entry(SessionKey { id })
            .get()
            .await
            .expect_err("invalid");
        assert!(matches!(err, Error::InvalidKvValue { .. }), "{err}");
        assert_eq!(cache.entry(Lenient { id }).get().await.unwrap(), None);
    }
    assert_eq!(
        cache
            .get_many(&[Lenient { id: 3 }, Lenient { id: 4 }])
            .await
            .unwrap(),
        [None, None]
    );
}

#[tokio::test]
async fn a_list_keeps_its_values_in_order() {
    let (cache, _server) = serve("list");
    let recent = || cache.entry(Recent { user: "ada".into() });

    assert_eq!(recent().push_back(["b", "c"]).await.unwrap(), 2);
    assert_eq!(recent().push_front(["z", "a"]).await.unwrap(), 4);
    assert_eq!(recent().range(0, -1).await.unwrap(), ["z", "a", "b", "c"]);
    assert_eq!(recent().range(1, 2).await.unwrap(), ["a", "b"]);
    assert_eq!(recent().get(-1).await.unwrap().as_deref(), Some("c"));
    assert_eq!(recent().get(9).await.unwrap(), None);
    assert_eq!(recent().pop_back().await.unwrap().as_deref(), Some("c"));
    assert_eq!(recent().pop_front().await.unwrap().as_deref(), Some("z"));
    assert_eq!(recent().len().await.unwrap(), 2);
    assert!(!recent().is_empty().await.unwrap());
    assert!(recent().delete().await.unwrap());
    assert_eq!(recent().pop_back().await.unwrap(), None);
    assert!(recent().is_empty().await.unwrap());
}

#[tokio::test]
async fn a_set_keeps_distinct_members() {
    let (cache, _server) = serve("set");
    let online = || {
        cache.entry(Online {
            room: "lobby".into(),
        })
    };

    assert_eq!(online().insert(["ada", "bob"]).await.unwrap(), 2);
    assert_eq!(online().insert(["ada"]).await.unwrap(), 0);
    assert!(online().contains("ada").await.unwrap());
    assert_eq!(online().len().await.unwrap(), 2);
    assert_eq!(
        online().members().await.unwrap(),
        HashSet::from(["ada".to_string(), "bob".to_string()])
    );
    assert_eq!(online().remove(["ada"]).await.unwrap(), 1);
    assert!(online().delete().await.unwrap());
    assert!(online().is_empty().await.unwrap());
}

#[tokio::test]
async fn a_list_or_set_write_of_no_values_is_refused_before_the_store_is_reached() {
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(
        "OCEL_RESOURCE_KV_unreachable",
        r#"{"name":"kv--unreachable","kv":{"host":"127.0.0.1","port":1}}"#,
    );
    let cache = store("unreachable");
    let recent = cache.entry(Recent { user: "ada".into() });
    let online = cache.entry(Online {
        room: "lobby".into(),
    });
    let none: [&str; 0] = [];

    for (access, err) in [
        (
            "recent.push_back",
            recent.push_back(none).await.unwrap_err(),
        ),
        (
            "recent.push_front",
            recent.push_front(none).await.unwrap_err(),
        ),
        ("online.insert", online.insert(none).await.unwrap_err()),
        ("online.remove", online.remove(none).await.unwrap_err()),
    ] {
        assert!(
            matches!(err, Error::EmptyKvWrite { access: ref refused } if refused == access),
            "{access}: {err}"
        );
    }
}

#[tokio::test]
async fn a_declared_ttl_is_applied_with_every_write() {
    let (cache, server) = serve("ttl");

    cache
        .entry(ExpiringSession { id: 1 })
        .set("x")
        .await
        .unwrap();
    cache
        .entry(ExpiringHits { id: 1 })
        .increment(1)
        .await
        .unwrap();
    cache
        .entry(ExpiringRecent { id: 1 })
        .push_back(["x"])
        .await
        .unwrap();
    cache
        .entry(ExpiringOnline { id: 1 })
        .insert(["x"])
        .await
        .unwrap();

    assert_eq!(server.seconds_left("ttl/session/1"), Some(30 * 86400));
    assert_eq!(server.seconds_left("ttl/hits/1"), Some(10));
    assert_eq!(server.seconds_left("ttl/recent/1"), Some(3600));
    assert_eq!(server.seconds_left("ttl/online/1"), Some(300));
}

#[tokio::test]
async fn a_write_overrides_keeps_or_clears_the_ttl() {
    let (cache, server) = serve("ttlwrite");
    let session = || cache.entry(ExpiringSession { id: 2 });
    let hits = || cache.entry(ExpiringHits { id: 2 });

    session()
        .set("x")
        .ttl(Duration::from_secs(5))
        .await
        .unwrap();
    assert_eq!(server.seconds_left("ttl/session/2"), Some(5));
    session().set("y").keep_ttl().await.unwrap();
    assert_eq!(server.seconds_left("ttl/session/2"), Some(5));
    session().set("z").clear_ttl().await.unwrap();
    assert_eq!(server.seconds_left("ttl/session/2"), None);

    hits()
        .increment(1)
        .ttl(Duration::from_secs(60))
        .await
        .unwrap();
    hits().increment(1).keep_ttl().await.unwrap();
    assert_eq!(server.seconds_left("ttl/hits/2"), Some(60));
    hits().decrement(1).clear_ttl().await.unwrap();
    assert_eq!(server.seconds_left("ttl/hits/2"), None);

    server.set_text("notes/a", "x");
    server.expire("notes/a", Duration::from_secs(1));
    cache.entry(Note { id: "a".into() }).set("y").await.unwrap();
    assert_eq!(server.seconds_left("notes/a"), None);
}

#[tokio::test]
async fn a_client_is_built_once_per_store_and_shared() {
    let (cache, server) = serve("client");

    let client = cache.client().unwrap();
    let mut connection = client.get_multiplexed_async_connection().await.unwrap();
    let _: () = redis::cmd("SET")
        .arg("hello")
        .arg("world")
        .query_async(&mut connection)
        .await
        .unwrap();
    assert_eq!(server.text("hello").as_deref(), Some("world"));
    assert_eq!(
        cache.client().unwrap().get_connection_info().addr(),
        client.get_connection_info().addr()
    );
}

#[tokio::test]
async fn a_binding_that_requires_tls_is_reached_over_tls() {
    let server = Valkey::start();
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(
        "OCEL_RESOURCE_KV_tls",
        format!(
            r#"{{"name":"kv--tls","kv":{{"host":"127.0.0.1","port":{},"password":"pw","tls":true}}}}"#,
            server.port
        ),
    );

    let err = store("tls")
        .entry(Greeting)
        .get()
        .await
        .expect_err("a plaintext server refuses the TLS handshake");
    assert!(matches!(err, Error::Kv(_)), "{err}");
}

#[test]
fn a_client_over_a_port_that_is_no_tcp_port_is_refused() {
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(
        "OCEL_RESOURCE_KV_negativeport",
        r#"{"name":"kv--negativeport","kv":{"host":"127.0.0.1","port":-1}}"#,
    );

    let err = Kv::new("negativeport")
        .client()
        .expect_err("a negative port");
    assert!(
        matches!(err, Error::InvalidKvPort { port: -1, .. }),
        "{err}"
    );
}

#[tokio::test]
async fn a_write_shorter_than_a_millisecond_is_refused() {
    let (cache, _server) = serve("shortttl");

    let err = cache
        .entry(Greeting)
        .set("x")
        .ttl(Duration::from_micros(10))
        .await
        .expect_err("a ttl under 1ms");
    assert!(matches!(err, Error::InvalidKvTtl { .. }), "{err}");
}

#[test]
fn a_client_opens_trusting_the_authority_the_kv_binding_fixture_delivers() {
    let fixture = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../proto/common/bindings/v1/fixtures/kv.json");
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(
        "OCEL_RESOURCE_KV_trusted",
        std::fs::read_to_string(fixture)
            .expect("the kv binding fixture")
            .replace("kv--cache", "kv--trusted"),
    );

    Kv::new("trusted")
        .client()
        .expect("a client trusting the fixture's caPem");
}

#[test]
fn a_client_is_refused_when_the_delivered_authority_holds_no_certificate() {
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(
        "OCEL_RESOURCE_KV_garbled",
        r#"{"name":"kv--garbled","kv":{"host":"10.240.0.5","port":6378,"password":"pw","tls":true,"caPem":"not a certificate"}}"#,
    );

    let err = Kv::new("garbled").client().unwrap_err();
    assert!(
        matches!(err, Error::InvalidKvAuthority { ref key } if key == "OCEL_RESOURCE_KV_garbled"),
        "{err}"
    );
}

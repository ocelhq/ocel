#![cfg(feature = "kv")]

mod valkey;

use ocel::Kv;
use serde_json::Value as Json;
use std::collections::{BTreeSet, HashSet, VecDeque};
use valkey::{Valkey, Value};

#[derive(Debug, PartialEq, serde::Serialize, serde::Deserialize)]
struct Session {
    user: String,
    roles: Vec<String>,
    visits: u32,
    note: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "config", text)]
struct Config;

#[derive(ocel::KvKey)]
#[ocel(pattern = "requests/:userId", text)]
#[allow(non_snake_case)]
struct Requests {
    userId: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text)]
struct SessionText {
    id: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "rooms/:room/members/:member", text)]
struct Member {
    room: String,
    member: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "files/:name", text)]
struct File {
    name: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "q/:query", text)]
struct Query {
    query: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "emoji/:e", text)]
struct Emoji {
    e: String,
}

#[derive(ocel::KvKey)]
#[ocel(pattern = "text", text)]
struct TextValue;

#[derive(ocel::KvKey)]
#[ocel(pattern = "counter", counter)]
struct CounterValue;

#[derive(ocel::KvKey)]
#[ocel(pattern = "json", json = Session)]
struct JsonValue;

#[derive(ocel::KvKey)]
#[ocel(pattern = "list", list)]
struct ListValue;

#[derive(ocel::KvKey)]
#[ocel(pattern = "set", set)]
struct SetValue;

fn fixture() -> Json {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../proto/app/resources/v1/fixtures/kv.json");
    serde_json::from_str(&std::fs::read_to_string(path).expect("the kv fixture"))
        .expect("the kv fixture is json")
}

fn serve(name: &str) -> (Kv, Valkey) {
    let server = Valkey::start();
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var(format!("OCEL_RESOURCE_KV_{name}"), server.binding(name));
    let cache = Kv::new(name)
        .with_entry::<Config>()
        .with_entry::<Requests>()
        .with_entry::<SessionText>()
        .with_entry::<Member>()
        .with_entry::<File>()
        .with_entry::<Query>()
        .with_entry::<Emoji>()
        .with_entry::<TextValue>()
        .with_entry::<CounterValue>()
        .with_entry::<JsonValue>()
        .with_entry::<ListValue>()
        .with_entry::<SetValue>();
    (cache, server)
}

fn param(params: &Json, name: &str) -> String {
    params[name]
        .as_str()
        .expect("a string parameter")
        .to_string()
}

#[tokio::test]
async fn the_fixture_keys_are_built_as_every_sdk_builds_them() {
    for case in fixture()["keys"].as_array().expect("keys") {
        let (pattern, params, key) = (
            case["pattern"].as_str().unwrap(),
            &case["params"],
            case["key"].as_str().unwrap(),
        );
        let (cache, server) = serve("keys");
        let (built, written) = match pattern {
            "config" => (
                cache.entry(Config).key().to_string(),
                cache.entry(Config).set("written").await,
            ),
            "requests/:userId" => {
                let entry = cache.entry(Requests {
                    userId: param(params, "userId"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            "session/:id" => {
                let entry = cache.entry(SessionText {
                    id: param(params, "id"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            "rooms/:room/members/:member" => {
                let entry = cache.entry(Member {
                    room: param(params, "room"),
                    member: param(params, "member"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            "files/:name" => {
                let entry = cache.entry(File {
                    name: param(params, "name"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            "q/:query" => {
                let entry = cache.entry(Query {
                    query: param(params, "query"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            "emoji/:e" => {
                let entry = cache.entry(Emoji {
                    e: param(params, "e"),
                });
                (entry.key().to_string(), entry.set("written").await)
            }
            other => {
                panic!("the fixture holds pattern {other}, which this test has no key type for")
            }
        };
        written.expect("the write");
        assert_eq!(built, key, "{pattern}");
        assert_eq!(server.keys(), [key], "{pattern}");
    }
}

#[tokio::test]
async fn the_fixture_values_are_stored_as_every_sdk_stores_them() {
    let fixture = fixture();
    let values = &fixture["values"];
    let (cache, server) = serve("values");

    for case in values["text"].as_array().unwrap() {
        let (value, stored) = (
            case["value"].as_str().unwrap(),
            case["stored"].as_str().unwrap(),
        );
        cache.entry(TextValue).set(value).await.unwrap();
        assert_eq!(server.text("text").as_deref(), Some(stored));
        server.set_text("text", stored);
        assert_eq!(
            cache.entry(TextValue).get().await.unwrap().as_deref(),
            Some(value)
        );
    }
    for case in values["counter"].as_array().unwrap() {
        let (value, stored) = (
            case["value"].as_i64().unwrap(),
            case["stored"].as_str().unwrap(),
        );
        cache.entry(CounterValue).set(value).await.unwrap();
        assert_eq!(server.text("counter").as_deref(), Some(stored));
        server.set_text("counter", stored);
        assert_eq!(cache.entry(CounterValue).get().await.unwrap(), Some(value));
    }
    for case in values["json"].as_array().unwrap() {
        let value: Session = serde_json::from_value(case["value"].clone()).unwrap();
        let stored = case["stored"].as_str().unwrap();
        cache.entry(JsonValue).set(&value).await.unwrap();
        assert_eq!(server.text("json").as_deref(), Some(stored));
        server.set_text("json", stored);
        assert_eq!(cache.entry(JsonValue).get().await.unwrap(), Some(value));
    }
    for case in values["list"].as_array().unwrap() {
        let value: Vec<String> = serde_json::from_value(case["value"].clone()).unwrap();
        let stored: VecDeque<String> = serde_json::from_value(case["stored"].clone()).unwrap();
        cache
            .entry(ListValue)
            .push_back(value.clone())
            .await
            .unwrap();
        assert_eq!(server.get("list"), Some(Value::List(stored.clone())));
        server.set("list", Value::List(stored));
        assert_eq!(cache.entry(ListValue).range(0, -1).await.unwrap(), value);
    }
    for case in values["set"].as_array().unwrap() {
        let value: HashSet<String> = serde_json::from_value(case["value"].clone()).unwrap();
        let stored: BTreeSet<String> = serde_json::from_value(case["stored"].clone()).unwrap();
        cache.entry(SetValue).insert(value.clone()).await.unwrap();
        assert_eq!(server.get("set"), Some(Value::Set(stored.clone())));
        server.set("set", Value::Set(stored));
        assert_eq!(cache.entry(SetValue).members().await.unwrap(), value);
    }
}

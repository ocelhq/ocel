#![cfg(all(feature = "schemars", feature = "realtime"))]

mod realtime;
mod runtime;

use ocel::realtime::{DenialCode, Realtime};
use realtime::{post, FakeRuntime};
use serde_json::json;

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema)]
struct Note {
    #[schemars(length(max = 5))]
    text: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "notes/:note_id", event = Note, public, publish, schema)]
struct Notes {
    note_id: String,
}

#[derive(serde::Serialize)]
struct Caller {
    id: String,
}

fn build() -> Realtime {
    Realtime::builder("app")
        .authorize(|_| async {
            Ok::<_, String>(Some(Caller {
                id: "u1".to_string(),
            }))
        })
        .publish::<Notes>(|_| async { Ok::<_, String>(true) })
        .build()
        .expect("the realtime resource builds")
}

#[tokio::test]
async fn an_event_its_channel_schema_refuses_is_never_published() {
    let runtime = FakeRuntime::serve();
    std::env::set_var("OCEL_RESOURCE_REALTIME_app", runtime.binding());
    let rt = build();
    let note = |text: &str| Note {
        text: text.to_string(),
    };

    let refused = rt
        .publish(
            &Notes {
                note_id: "n1".to_string(),
            },
            &note("too long"),
        )
        .await;
    let relayed = post(
        &rt,
        json!({ "ops": [
            { "op": "publish", "pattern": "notes/:note_id", "params": { "note_id": "n1" }, "body": { "text": "too long" } },
            { "op": "publish", "pattern": "notes/:note_id", "params": { "note_id": "n1" }, "body": { "text": "ok" } },
        ] }),
        &[],
    )
    .await;

    assert!(
        matches!(refused, Err(ocel::Error::PublishRefused { code, .. }) if code == DenialCode::InvalidBody)
    );
    assert_eq!(
        relayed["denied"],
        json!([{ "i": 0, "code": "invalid-body" }])
    );
    assert_eq!(
        relayed["grants"],
        json!([{ "i": 1, "wire": "/app/notes/n1" }])
    );
    let published = runtime.published();
    assert_eq!(published.len(), 1);
    assert_eq!(published[0].envelope["data"], json!({ "text": "ok" }));
}

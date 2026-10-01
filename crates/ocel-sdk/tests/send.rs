mod runtime;

use ocel::proto::app::topic::v1::{
    CountDeadLettersRequest, CountDeadLettersResponse, DeadLetter, Lane, ListDeadLettersRequest,
    ListDeadLettersResponse, Message, PurgeDeadLettersRequest, PurgeDeadLettersResponse,
    RedriveDeadLettersRequest, RedriveDeadLettersResponse, SendRequest, SendResponse,
};
use runtime::{runtime, TOKEN};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

#[derive(serde::Serialize, serde::Deserialize)]
pub struct Order {
    pub id: u64,
}

#[derive(ocel::Resources)]
struct Infra {
    orders: ocel::Topic<Order>,
}

fn orders() -> ocel::Topic<Order> {
    std::env::set_var(
        "OCEL_RESOURCE_TOPIC_orders",
        r#"{"name":"orders","topic":{"topic":"orders-91c2"}}"#,
    );
    Infra::load().expect("the struct loads").orders
}

#[tokio::test]
async fn a_send_carries_the_payload_as_json_to_the_bound_topic_with_its_options() {
    let runtime = runtime();
    runtime.answer(
        "Send",
        SendResponse {
            message_id: "01J0000000000000000000000A".into(),
            ..Default::default()
        },
    );
    let before = SystemTime::now();
    let id = orders()
        .send(Order { id: 7 })
        .delay(Duration::from_secs(30))
        .idempotency_key("order-7")
        .key("customer-1")
        .lane(ocel::Lane::Low)
        .await
        .expect("the send");
    assert_eq!(id, "01J0000000000000000000000A");

    let calls = runtime.only("Send");
    assert_eq!(calls[0].path, "/app.topic.v1.TopicService/Send");
    assert_eq!(calls[0].authorization, Some(format!("Bearer {TOKEN}")));
    let sent: SendRequest = calls[0].decode();
    assert_eq!(sent.topic, "orders-91c2");
    assert_eq!(sent.payload, br#"{"id":7}"#);
    let due = sent.due_at.as_option().expect("a due time").seconds;
    let since = |at: SystemTime| at.duration_since(UNIX_EPOCH).expect("time").as_secs() as i64;
    assert!((since(before) + 30..=since(SystemTime::now()) + 30).contains(&due));
    assert_eq!(sent.idempotency_key, "order-7");
    assert_eq!(sent.key, "customer-1");
    assert_eq!(sent.lane.as_known(), Some(Lane::LANE_LOW));
}

#[tokio::test]
async fn a_send_refused_by_the_runtime_names_the_topic_and_the_access() {
    let runtime = runtime();
    runtime.refuse("Send", 400, "invalid_argument", "due_at is too far away");
    let err = orders()
        .send(Order { id: 7 })
        .await
        .expect_err("a refused send");
    assert!(matches!(err, ocel::Error::RuntimeRefused { .. }), "{err}");
    assert!(
        err.to_string()
            .starts_with("'topic(\"orders\")' send was refused by the runtime:"),
        "{err}"
    );
    assert!(err.to_string().contains("due_at is too far away"), "{err}");
}

#[tokio::test]
async fn a_consumers_dead_letters_are_listed_redriven_purged_and_counted() {
    let runtime = runtime();
    runtime.answer(
        "ListDeadLetters",
        ListDeadLettersResponse {
            dead_letters: vec![DeadLetter {
                execution: "01J0000000000000000000000A-email".into(),
                message: Message {
                    id: "01J0000000000000000000000A".into(),
                    published_at: buffa_types::google::protobuf::Timestamp::from_unix_secs(
                        1_700_000_000,
                    )
                    .into(),
                    ..Default::default()
                }
                .into(),
                payload: serde_json::from_value(serde_json::json!({ "id": 7 })).expect("a value"),
                attempts: 3,
                error: "smtp refused".into(),
                ..Default::default()
            }],
            next_cursor: String::new(),
            ..Default::default()
        },
    );
    runtime.answer(
        "RedriveDeadLetters",
        RedriveDeadLettersResponse {
            redriven: 1,
            ..Default::default()
        },
    );
    runtime.answer(
        "PurgeDeadLetters",
        PurgeDeadLettersResponse {
            purged: 4,
            ..Default::default()
        },
    );
    runtime.answer(
        "CountDeadLetters",
        CountDeadLettersResponse {
            count: 9,
            ..Default::default()
        },
    );
    let orders = orders();
    let email = orders.dead_letter("email");

    let page = email.list().limit(10).cursor("c1").await.expect("a page");
    assert_eq!(page.next_cursor, None);
    let letter = &page.dead_letters[0];
    assert_eq!(letter.execution, "01J0000000000000000000000A-email");
    assert_eq!(letter.message.id, "01J0000000000000000000000A");
    assert_eq!(
        letter.message.published_at,
        Some(UNIX_EPOCH + Duration::from_secs(1_700_000_000))
    );
    assert_eq!(letter.payload, serde_json::json!({ "id": 7 }));
    assert_eq!(
        (letter.attempts, letter.error.as_str()),
        (3, "smtp refused")
    );

    assert_eq!(
        email
            .redrive(["01J0000000000000000000000A-email"])
            .await
            .expect("a redrive"),
        1
    );
    assert_eq!(email.purge_all().await.expect("a purge"), 4);
    assert_eq!(email.count().await.expect("a count"), 9);
    assert_eq!(
        email
            .redrive(Vec::<String>::new())
            .await
            .expect("an empty redrive"),
        0
    );

    let list: ListDeadLettersRequest = runtime.only("ListDeadLetters")[0].decode();
    assert_eq!(
        (
            list.topic.as_str(),
            list.consumer.as_str(),
            list.cursor.as_str(),
            list.limit
        ),
        ("orders-91c2", "email", "c1", 10)
    );
    let redrives = runtime.only("RedriveDeadLetters");
    assert_eq!(redrives.len(), 1);
    let redrive: RedriveDeadLettersRequest = redrives[0].decode();
    assert_eq!(redrive.executions, ["01J0000000000000000000000A-email"]);
    let purge: PurgeDeadLettersRequest = runtime.only("PurgeDeadLetters")[0].decode();
    assert!(purge.executions.is_empty());
    let count: CountDeadLettersRequest = runtime.only("CountDeadLetters")[0].decode();
    assert_eq!(
        (count.topic.as_str(), count.consumer.as_str()),
        ("orders-91c2", "email")
    );
}

#[tokio::test]
async fn a_send_during_discovery_names_the_topic_and_the_access() {
    let _runtime = runtime();
    let orders = orders();
    std::env::set_var("OCEL_PHASE", "discovery");
    let err = orders
        .send(Order { id: 7 })
        .await
        .expect_err("discovery provisions nothing");
    std::env::remove_var("OCEL_PHASE");
    assert_eq!(
        err.to_string(),
        "'topic(\"orders\")' cannot be used during discovery: tried to access 'send' before the resource was provisioned"
    );
}

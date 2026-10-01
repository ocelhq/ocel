mod runtime;

use ocel::proto::app::task::v1::{
    BatchTriggerRequest, BatchTriggerResponse, CancelRunRequest, CancelRunResponse,
    ListRunsRequest, ListRunsResponse, ReplayRunRequest, ReplayRunResponse, RescheduleRunRequest,
    RescheduleRunResponse, RetrieveRunResponse, Run, RunStatus, TriggerRequest, TriggerResponse,
};
use ocel::proto::app::topic::v1::Lane;
use runtime::{runtime, TOKEN};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

#[derive(serde::Serialize, serde::Deserialize)]
pub struct Image {
    pub url: String,
}

#[ocel::task(name = "resize-image")]
async fn resize(image: Image, _run: &ocel::Run) -> Result<usize, ocel::RunError> {
    Ok(image.url.len())
}

#[ocel::task]
async fn other_task(_image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn bind_task() {
    std::env::set_var(
        "OCEL_RESOURCE_TASK_resize-image",
        r#"{"name":"resize-image","task":{}}"#,
    );
}

fn new_image(url: &str) -> Image {
    Image {
        url: url.to_string(),
    }
}

fn read_seconds(at: SystemTime) -> i64 {
    at.duration_since(UNIX_EPOCH)
        .expect("after the epoch")
        .as_secs() as i64
}

#[tokio::test]
async fn a_trigger_sends_the_payload_as_json_to_the_task_by_its_declared_name_with_its_options() {
    let runtime = runtime();
    bind_task();
    runtime.answer(
        "Trigger",
        TriggerResponse {
            id: "run_1".into(),
            ..Default::default()
        },
    );
    let mut metadata = serde_json::Map::new();
    metadata.insert("source".into(), serde_json::json!("upload"));

    let before = SystemTime::now();
    let run = resize
        .trigger(new_image("a.png"))
        .delay(Duration::from_secs(60))
        .ttl(Duration::from_secs(3600))
        .idempotency_key("upload-1")
        .idempotency_key_ttl(Duration::from_secs(86400))
        .debounce("user-1", Duration::from_secs(5))
        .key("user-1")
        .lane(ocel::Lane::High)
        .max_attempts(2)
        .tags(["user:1", "png"])
        .metadata(metadata)
        .await
        .expect("the trigger");
    assert_eq!(run, ocel::RunHandle { id: "run_1".into() });

    let calls = runtime.only("Trigger");
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0].path, "/app.task.v1.TaskService/Trigger");
    assert_eq!(calls[0].authorization, Some(format!("Bearer {TOKEN}")));
    let sent: TriggerRequest = calls[0].decode();
    assert_eq!(sent.task, "resize-image");
    assert_eq!(
        serde_json::from_slice::<serde_json::Value>(&sent.payload).expect("json"),
        serde_json::json!({ "url": "a.png" })
    );
    let options = sent.options.as_option().expect("options");
    let due = options.due_at.as_option().expect("a due time").seconds;
    assert!((read_seconds(before) + 60..=read_seconds(SystemTime::now()) + 60).contains(&due));
    assert_eq!(options.ttl.as_option().expect("a ttl").seconds, 3600);
    assert_eq!(options.idempotency_key, "upload-1");
    assert_eq!(
        options
            .idempotency_key_ttl
            .as_option()
            .expect("a key ttl")
            .seconds,
        86400
    );
    let debounce = options.debounce.as_option().expect("a debounce");
    assert_eq!(debounce.key, "user-1");
    assert_eq!(debounce.delay.as_option().expect("a delay").seconds, 5);
    assert_eq!(options.key, "user-1");
    assert_eq!(options.lane.as_known(), Some(Lane::LANE_HIGH));
    assert_eq!(options.max_attempts, 2);
    assert_eq!(options.tags, ["user:1", "png"]);
    assert_eq!(
        serde_json::to_value(options.metadata.as_option().expect("metadata")).expect("json"),
        serde_json::json!({ "source": "upload" })
    );
}

#[tokio::test]
async fn a_trigger_with_no_options_sends_none_and_a_time_sends_it_as_the_due_time() {
    let runtime = runtime();
    bind_task();
    runtime.answer("Trigger", TriggerResponse::default());
    resize
        .trigger(new_image("a.png"))
        .await
        .expect("the trigger");
    let at = UNIX_EPOCH + Duration::from_secs(2_000_000_000);
    resize
        .trigger(new_image("b.png"))
        .at(at)
        .await
        .expect("the trigger");

    let calls = runtime.only("Trigger");
    let plain: TriggerRequest = calls[0].decode();
    let options = plain.options.as_option().cloned().unwrap_or_default();
    assert!(options.due_at.as_option().is_none());
    assert!(options.debounce.as_option().is_none());
    assert_eq!(options.lane.as_known(), Some(Lane::LANE_UNSPECIFIED));
    let timed: TriggerRequest = calls[1].decode();
    let options = timed.options.as_option().expect("options");
    assert_eq!(
        options.due_at.as_option().expect("a due time").seconds,
        2_000_000_000
    );
}

#[tokio::test]
async fn a_batch_trigger_sends_every_payload_in_order_and_answers_their_ids() {
    let runtime = runtime();
    bind_task();
    runtime.answer(
        "BatchTrigger",
        BatchTriggerResponse {
            ids: vec!["run_1".into(), "run_2".into()],
            ..Default::default()
        },
    );
    let runs = resize
        .batch_trigger([
            resize.trigger(new_image("a.png")).key("a"),
            resize.trigger(new_image("b.png")),
        ])
        .await
        .expect("the batch");
    assert_eq!(
        runs.iter().map(|run| run.id.as_str()).collect::<Vec<_>>(),
        ["run_1", "run_2"]
    );

    let sent: BatchTriggerRequest = runtime.only("BatchTrigger")[0].decode();
    assert_eq!(sent.task, "resize-image");
    let urls: Vec<serde_json::Value> = sent
        .items
        .iter()
        .map(|item| serde_json::from_slice(&item.payload).expect("json"))
        .collect();
    assert_eq!(
        urls,
        [
            serde_json::json!({ "url": "a.png" }),
            serde_json::json!({ "url": "b.png" })
        ]
    );
    assert_eq!(sent.items[0].options.as_option().expect("options").key, "a");
}

#[tokio::test]
async fn a_batch_holding_another_tasks_trigger_is_refused_before_anything_is_sent() {
    let runtime = runtime();
    bind_task();
    let err = resize
        .batch_trigger([resize.trigger(new_image("a.png")), another_tasks_trigger()])
        .await
        .expect_err("a mixed batch");
    assert_eq!(
        err.to_string(),
        "a batch triggering 'resize-image' contains a trigger of 'other-task': every trigger in a batch is built from the task the batch is sent to"
    );
    assert!(runtime.calls().is_empty());
}

fn another_tasks_trigger() -> ocel::Trigger<'static, Image, usize> {
    static IMPOSTOR: ocel::Task<Image, usize> = ocel::Task::new("other-task");
    let _ = &other_task;
    IMPOSTOR.trigger(new_image("c.png"))
}

#[tokio::test]
async fn a_payload_over_256_kib_is_refused_before_anything_is_sent() {
    let runtime = runtime();
    bind_task();
    let err = resize
        .trigger(new_image(&"x".repeat(262_144)))
        .await
        .expect_err("an oversized payload");
    assert!(matches!(err, ocel::Error::PayloadTooLarge { .. }), "{err}");
    assert_eq!(
        err.to_string(),
        "the payload is 262154 bytes of JSON, and a payload is at most 262144 bytes (256 KiB)"
    );
    assert!(runtime.calls().is_empty());
}

#[tokio::test]
async fn a_trigger_during_discovery_names_the_task_and_the_access() {
    let _runtime = runtime();
    std::env::set_var("OCEL_PHASE", "discovery");
    let err = resize
        .trigger(new_image("a.png"))
        .await
        .expect_err("discovery provisions nothing");
    std::env::remove_var("OCEL_PHASE");
    assert_eq!(
        err.to_string(),
        "'task(\"resize-image\")' cannot be used during discovery: tried to access 'trigger' before the resource was provisioned"
    );
}

fn build_wire_run(id: &str) -> Run {
    Run {
        id: id.into(),
        task: "resize-image".into(),
        status: RunStatus::RUN_STATUS_COMPLETED.into(),
        payload: serde_json::from_value(serde_json::json!({ "url": "a.png" })).expect("a value"),
        output: serde_json::from_value(serde_json::json!(5)).expect("a value"),
        attempts: 2,
        tags: vec!["png".into()],
        metadata: serde_json::from_value(serde_json::json!({ "source": "upload" }))
            .expect("a struct"),
        created_at: buffa_types::google::protobuf::Timestamp::from_unix_secs(1_700_000_000).into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn a_retrieved_run_comes_back_with_its_payload_and_output_as_json() {
    let runtime = runtime();
    runtime.answer(
        "RetrieveRun",
        RetrieveRunResponse {
            run: build_wire_run("run_1").into(),
            ..Default::default()
        },
    );
    let run = ocel::runs::retrieve("run_1").await.expect("the run");
    assert_eq!(run.id, "run_1");
    assert_eq!(run.status, Some(ocel::runs::RunStatus::Completed));
    assert_eq!(run.payload, serde_json::json!({ "url": "a.png" }));
    assert_eq!(run.output, serde_json::json!(5));
    assert_eq!(run.attempts, 2);
    assert_eq!(run.tags, ["png"]);
    assert_eq!(run.metadata["source"], "upload");
    assert_eq!(
        run.created_at,
        Some(UNIX_EPOCH + Duration::from_secs(1_700_000_000))
    );
    assert_eq!(run.finished_at, None);
}

#[tokio::test]
async fn a_run_the_runtime_does_not_have_is_named_by_its_id() {
    let runtime = runtime();
    runtime.refuse("RetrieveRun", 404, "not_found", "no such run");
    let err = ocel::runs::retrieve("run_404").await.expect_err("no run");
    assert!(matches!(err, ocel::Error::UnknownRun { .. }), "{err}");
    assert_eq!(err.to_string(), "no run has the id 'run_404'");
}

#[tokio::test]
async fn a_listing_of_a_tasks_runs_names_the_declared_task_and_pages_by_cursor() {
    let runtime = runtime();
    bind_task();
    runtime.answer(
        "ListRuns",
        ListRunsResponse {
            runs: vec![build_wire_run("run_1"), build_wire_run("run_2")],
            next_cursor: "page-2".into(),
            ..Default::default()
        },
    );
    let page = ocel::runs::list()
        .task(resize.name())
        .status(ocel::runs::RunStatus::Failed)
        .status(ocel::runs::RunStatus::TimedOut)
        .tags(["png"])
        .cursor("page-1")
        .limit(2)
        .await
        .expect("a page");
    assert_eq!(
        page.runs
            .iter()
            .map(|run| run.id.as_str())
            .collect::<Vec<_>>(),
        ["run_1", "run_2"]
    );
    assert_eq!(page.next_cursor.as_deref(), Some("page-2"));

    let sent: ListRunsRequest = runtime.only("ListRuns")[0].decode();
    assert_eq!(sent.task, "resize-image");
    assert_eq!(
        sent.statuses
            .iter()
            .map(|status| status.as_known())
            .collect::<Vec<_>>(),
        [
            Some(RunStatus::RUN_STATUS_FAILED),
            Some(RunStatus::RUN_STATUS_TIMED_OUT)
        ]
    );
    assert_eq!(sent.tags, ["png"]);
    assert_eq!((sent.cursor.as_str(), sent.limit), ("page-1", 2));
}

#[tokio::test]
async fn a_run_is_canceled_replayed_and_rescheduled_by_its_id() {
    let runtime = runtime();
    runtime.answer(
        "CancelRun",
        CancelRunResponse {
            run: build_wire_run("run_1").into(),
            ..Default::default()
        },
    );
    runtime.answer(
        "ReplayRun",
        ReplayRunResponse {
            id: "run_2".into(),
            ..Default::default()
        },
    );
    runtime.answer(
        "RescheduleRun",
        RescheduleRunResponse {
            run: build_wire_run("run_1").into(),
            ..Default::default()
        },
    );

    assert_eq!(
        ocel::runs::cancel("run_1").await.expect("a cancel").id,
        "run_1"
    );
    assert_eq!(
        ocel::runs::replay("run_1").await.expect("a replay").id,
        "run_2"
    );
    let before = SystemTime::now();
    ocel::runs::reschedule("run_1")
        .delay(Duration::from_secs(120))
        .await
        .expect("a reschedule");

    let cancel: CancelRunRequest = runtime.only("CancelRun")[0].decode();
    assert_eq!(cancel.id, "run_1");
    let replay: ReplayRunRequest = runtime.only("ReplayRun")[0].decode();
    assert_eq!(replay.id, "run_1");
    let reschedule: RescheduleRunRequest = runtime.only("RescheduleRun")[0].decode();
    assert_eq!(reschedule.id, "run_1");
    let due = reschedule.due_at.as_option().expect("a due time").seconds;
    assert!((read_seconds(before) + 120..=read_seconds(SystemTime::now()) + 120).contains(&due));
}

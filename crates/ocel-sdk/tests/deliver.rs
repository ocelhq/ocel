use serde_json::{json, Value};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Mutex;
use std::time::{Duration, UNIX_EPOCH};

#[derive(Clone, serde::Serialize, serde::Deserialize)]
pub struct Image {
    pub url: String,
}

fn build_envelope(topic: &str, consumer: &str, payload: Value, attempt: (u32, u32)) -> Vec<u8> {
    serde_json::to_vec(&json!({
        "v": 1,
        "topic": topic,
        "consumer": consumer,
        "execution": "run_1",
        "message": { "id": "01J0000000000000000000000A", "publishedAt": "2023-11-14T22:13:20Z" },
        "attempt": { "number": attempt.0, "of": attempt.1, "firstAttemptedAt": "2023-11-14T22:13:20Z" },
        "payload": payload,
    }))
    .expect("an envelope")
}

fn decode_answer((status, body): (u16, Vec<u8>)) -> (u16, String) {
    (status, String::from_utf8(body).expect("utf-8"))
}

async fn deliver_body(worker: &str, body: Vec<u8>) -> (u16, String) {
    decode_answer(ocel::deliver(worker, &body).await)
}

fn take_log(log: &Mutex<Vec<String>>) -> Vec<String> {
    std::mem::take(&mut *log.lock().expect("the log"))
}

fn record(log: &Mutex<Vec<String>>, entry: impl Into<String>) {
    log.lock().expect("the log").push(entry.into());
}

#[ocel::task]
async fn measure(image: Image, run: &ocel::Run) -> Result<Value, ocel::RunError> {
    Ok(json!({
        "length": image.url.len(),
        "kind": format!("{:?}", run.kind()),
        "name": run.name(),
        "topic": run.topic(),
        "id": run.id(),
        "attempt": [run.attempt().number, run.attempt().of],
        "first": run.attempt().first_attempted_at.map(|at| at.duration_since(UNIX_EPOCH).expect("time").as_secs()),
        "message": run.message().id,
        "published": run.message().published_at.map(|at| at.duration_since(UNIX_EPOCH).expect("time").as_secs()),
    }))
}

#[tokio::test]
async fn a_task_that_succeeds_answers_200_with_its_output_and_sees_its_run() {
    let (status, body) = deliver_body(
        "worker",
        build_envelope("measure", "measure", json!({ "url": "a.png" }), (2, 3)),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        serde_json::from_str::<Value>(&body).expect("json"),
        json!({
            "length": 5,
            "kind": "Task",
            "name": "measure",
            "topic": "measure",
            "id": "run_1",
            "attempt": [2, 3],
            "first": 1_700_000_000u64,
            "message": "01J0000000000000000000000A",
            "published": 1_700_000_000u64,
        })
    );
}

#[tokio::test]
async fn an_envelope_without_an_attempt_is_attempt_1_of_1() {
    let mut envelope: Value = serde_json::from_slice(&build_envelope(
        "measure",
        "measure",
        json!({ "url": "a" }),
        (2, 3),
    ))
    .expect("json");
    envelope
        .as_object_mut()
        .expect("an object")
        .remove("attempt");
    let (status, body) = deliver_body("worker", serde_json::to_vec(&envelope).expect("json")).await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        serde_json::from_str::<Value>(&body).expect("json")["attempt"],
        json!([1, 1])
    );
}

#[tokio::test]
async fn a_body_that_is_not_an_envelope_answers_400() {
    let (status, body) = deliver_body("worker", b"not json".to_vec()).await;
    assert_eq!(status, 400);
    assert!(
        body.starts_with("the body is not a delivery envelope"),
        "{body}"
    );
}

#[tokio::test]
async fn an_envelope_for_no_registered_consumer_answers_404_naming_it() {
    let (status, body) = deliver_body(
        "worker",
        build_envelope("nothing", "nobody", json!(null), (1, 1)),
    )
    .await;
    assert_eq!(status, 404);
    assert_eq!(
        body,
        "this binary registers no consumer 'nobody' of topic 'nothing'"
    );
}

#[tokio::test]
async fn an_envelope_for_a_consumer_of_another_worker_answers_404_naming_both_workers() {
    let (status, body) = deliver_body(
        "media",
        build_envelope("measure", "measure", json!({ "url": "a" }), (1, 1)),
    )
    .await;
    assert_eq!(status, 404);
    assert_eq!(
        body,
        "consumer 'measure' of topic 'measure' runs on worker 'worker', and this is worker 'media'"
    );
}

#[tokio::test]
async fn a_payload_that_does_not_match_the_declared_type_aborts() {
    let (status, body) = deliver_body(
        "worker",
        build_envelope("measure", "measure", json!({ "path": 1 }), (1, 3)),
    )
    .await;
    assert_eq!(status, 422);
    let reason = serde_json::from_str::<Value>(&body).expect("json")["abort"]["reason"]
        .as_str()
        .expect("a reason")
        .to_string();
    assert!(
        reason.starts_with("the payload does not match the declared type: missing field `url`"),
        "{reason}"
    );
}

static FAILING: Mutex<Vec<String>> = Mutex::new(Vec::new());
static FAILING_TURN: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

async fn failing_on_failure(
    image: &Image,
    error: &ocel::RunError,
    run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(
        &FAILING,
        format!(
            "on_failure {} {} {}",
            image.url,
            error,
            run.attempt().number
        ),
    );
    Ok(())
}

async fn failing_on_complete(
    image: &Image,
    result: Result<&(), &ocel::RunError>,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(
        &FAILING,
        format!(
            "on_complete {} {:?}",
            image.url,
            result.err().map(|err| err.to_string())
        ),
    );
    Err(ocel::RunError::new("a hook error changes nothing"))
}

#[ocel::task(on_failure = failing_on_failure, on_complete = failing_on_complete)]
async fn failing(image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    match image.url.as_str() {
        "abort" => Err(ocel::RunError::abort("the image is gone")),
        _ => Err(std::io::Error::other("the disk is full"))?,
    }
}

#[tokio::test]
async fn a_failed_attempt_answers_500_and_only_the_last_runs_the_failure_hooks() {
    let _turn = FAILING_TURN.lock().await;
    let (status, body) = deliver_body(
        "worker",
        build_envelope("failing", "failing", json!({ "url": "a" }), (1, 3)),
    )
    .await;
    assert_eq!((status, body.as_str()), (500, "the disk is full"));
    assert!(take_log(&FAILING).is_empty());

    let (status, body) = deliver_body(
        "worker",
        build_envelope("failing", "failing", json!({ "url": "a" }), (3, 3)),
    )
    .await;
    assert_eq!((status, body.as_str()), (500, "the disk is full"));
    assert_eq!(
        take_log(&FAILING),
        [
            "on_failure a the disk is full 3",
            "on_complete a Some(\"the disk is full\")"
        ]
    );
}

#[tokio::test]
async fn an_abort_answers_422_with_its_reason_and_runs_the_failure_hooks_on_any_attempt() {
    let _turn = FAILING_TURN.lock().await;
    let (status, body) = deliver_body(
        "worker",
        build_envelope("failing", "failing", json!({ "url": "abort" }), (1, 3)),
    )
    .await;
    assert_eq!(status, 422);
    assert_eq!(body, r#"{"abort":{"reason":"the image is gone"}}"#);
    assert_eq!(
        take_log(&FAILING),
        [
            "on_failure abort the image is gone 1",
            "on_complete abort Some(\"the image is gone\")"
        ]
    );
}

async fn give_up(_image: &Image, error: ocel::RunError, _run: &ocel::Run) -> ocel::RunError {
    match error.message() {
        "permanent" => error.into_abort(),
        _ => error,
    }
}

#[ocel::task(catch_error = give_up)]
async fn caught(image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Err(ocel::RunError::new(image.url))
}

#[tokio::test]
async fn catch_error_skips_the_remaining_attempts_by_turning_the_error_into_an_abort() {
    let (status, body) = deliver_body(
        "worker",
        build_envelope("caught", "caught", json!({ "url": "permanent" }), (1, 3)),
    )
    .await;
    assert_eq!(
        (status, body.as_str()),
        (422, r#"{"abort":{"reason":"permanent"}}"#)
    );
    let (status, body) = deliver_body(
        "worker",
        build_envelope("caught", "caught", json!({ "url": "transient" }), (1, 3)),
    )
    .await;
    assert_eq!((status, body.as_str()), (500, "transient"));
}

static ORDER: Mutex<Vec<String>> = Mutex::new(Vec::new());

async fn around_every_run(run: &ocel::Run, next: ocel::Next<'_>) -> Result<(), ocel::RunError> {
    record(
        &ORDER,
        format!("worker middleware {} {:?}", run.name(), run.kind()),
    );
    let result = next.await;
    record(&ORDER, "worker middleware done");
    result
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Workers {
    #[ocel(middleware = around_every_run)]
    media: ocel::Worker,
}

async fn before_attempt(image: &Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    record(&ORDER, format!("on_start_attempt {}", image.url));
    Ok(())
}

async fn around_run(
    _image: &Image,
    _run: &ocel::Run,
    next: ocel::Next<'_, u32>,
) -> Result<u32, ocel::RunError> {
    record(&ORDER, "task middleware");
    Ok(next.await? * 10)
}

async fn after_success(
    image: &Image,
    output: &u32,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(&ORDER, format!("on_success {} {output}", image.url));
    Ok(())
}

async fn after_complete(
    _image: &Image,
    result: Result<&u32, &ocel::RunError>,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(&ORDER, format!("on_complete {:?}", result.ok()));
    Ok(())
}

#[ocel::task(
    worker = "media",
    on_start_attempt = before_attempt,
    middleware = around_run,
    on_success = after_success,
    on_complete = after_complete
)]
async fn ordered_steps(image: Image, _run: &ocel::Run) -> Result<u32, ocel::RunError> {
    record(&ORDER, "run");
    Ok(image.url.len() as u32)
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Topics {
    uploads: ocel::Topic<Image>,
}

#[ocel::consumer(topic = Topics::UPLOADS, name = "thumbnail", worker = "media")]
async fn thumbnail(image: Image, run: &ocel::Run) -> Result<(), ocel::RunError> {
    record(&ORDER, format!("consumer {} {}", image.url, run.topic()));
    Ok(())
}

#[tokio::test]
async fn an_attempt_runs_inside_the_worker_middleware_then_the_task_hooks_in_order() {
    let (status, body) = deliver_body(
        "media",
        build_envelope(
            "ordered-steps",
            "ordered-steps",
            json!({ "url": "abc" }),
            (1, 3),
        ),
    )
    .await;
    assert_eq!((status, body.as_str()), (200, "30"));
    let (status, body) = deliver_body(
        "media",
        build_envelope("uploads", "thumbnail", json!({ "url": "x.png" }), (1, 3)),
    )
    .await;
    assert_eq!((status, body.as_str()), (200, "null"));
    assert_eq!(
        take_log(&ORDER),
        [
            "worker middleware ordered-steps Task",
            "on_start_attempt abc",
            "task middleware",
            "run",
            "worker middleware done",
            "on_success abc 30",
            "on_complete Some(30)",
            "worker middleware thumbnail Consumer",
            "consumer x.png uploads",
            "worker middleware done",
        ]
    );
}

#[ocel::task(batch(size = 10))]
async fn digest(images: Vec<Image>, _run: &ocel::Run) -> Result<Vec<String>, ocel::RunError> {
    Ok(images.into_iter().map(|image| image.url).collect())
}

#[tokio::test]
async fn a_batch_envelope_hands_the_task_every_message_in_order() {
    let body = serde_json::to_vec(&json!({
        "v": 1,
        "topic": "digest",
        "consumer": "digest",
        "messages": [
            { "execution": "run_a", "message": { "id": "01J0000000000000000000000A" }, "attempt": { "number": 1, "of": 3 }, "payload": { "url": "a" } },
            { "execution": "run_b", "message": { "id": "01J0000000000000000000000B" }, "attempt": { "number": 1, "of": 3 }, "payload": { "url": "b" } },
        ],
    }))
    .expect("an envelope");
    let (status, body) = deliver_body("worker", body).await;
    assert_eq!((status, body.as_str()), (200, r#"["a","b"]"#));
}

#[derive(serde::Deserialize)]
pub struct Reading {
    pub ratio: f64,
    pub id: u64,
    pub count: i64,
}

fn describe_reading(reading: &Reading) -> String {
    format!("{} {} {}", reading.ratio, reading.id, reading.count)
}

#[ocel::task]
async fn read_numbers(reading: Reading, _run: &ocel::Run) -> Result<String, ocel::RunError> {
    Ok(describe_reading(&reading))
}

#[ocel::task]
async fn inspect_numbers(numbers: Value, _run: &ocel::Run) -> Result<Value, ocel::RunError> {
    Ok(json!({
        "ratio_is_float": numbers["ratio"].is_f64(),
        "count_is_integer": numbers["count"].is_u64() && numbers["count"].is_i64(),
        "id": numbers["id"].as_u64().map(|id| id.to_string()),
    }))
}

#[ocel::task(batch(size = 10))]
async fn read_number_batch(
    readings: Vec<Reading>,
    _run: &ocel::Run,
) -> Result<Vec<String>, ocel::RunError> {
    Ok(readings.iter().map(describe_reading).collect())
}

const NUMBERS: &str = r#"{"ratio":2.0,"id":9007199254740993,"count":2}"#;

fn build_numbers_envelope(task: &str, payload: &str) -> Vec<u8> {
    format!(
        r#"{{"v":1,"topic":"{task}","consumer":"{task}","execution":"run_1","message":{{"id":"01J0000000000000000000000A"}},"attempt":{{"number":1,"of":1}},"payload":{payload}}}"#
    )
    .into_bytes()
}

#[tokio::test]
async fn a_delivered_payload_reaches_a_typed_handler_with_integers_above_2_to_the_53_exact() {
    let (status, body) =
        deliver_body("worker", build_numbers_envelope("read-numbers", NUMBERS)).await;
    assert_eq!((status, body.as_str()), (200, r#""2 9007199254740993 2""#));
}

#[tokio::test]
async fn a_delivered_payload_reaches_a_json_handler_with_floats_and_integers_kept_apart() {
    let (status, body) =
        deliver_body("worker", build_numbers_envelope("inspect-numbers", NUMBERS)).await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        serde_json::from_str::<Value>(&body).expect("json"),
        json!({ "ratio_is_float": true, "count_is_integer": true, "id": "9007199254740993" })
    );
}

#[tokio::test]
async fn a_delivered_float_is_not_coerced_into_an_integer_field() {
    let (status, body) = deliver_body(
        "worker",
        build_numbers_envelope("read-numbers", r#"{"ratio":2.0,"id":1,"count":2.0}"#),
    )
    .await;
    assert_eq!(status, 422, "{body}");
}

#[tokio::test]
async fn a_batch_envelope_hands_every_payload_over_with_integers_above_2_to_the_53_exact() {
    let body = format!(
        r#"{{"v":1,"topic":"read-number-batch","consumer":"read-number-batch","messages":[{{"execution":"run_a","message":{{"id":"01J0000000000000000000000A"}},"attempt":{{"number":1,"of":1}},"payload":{NUMBERS}}},{{"execution":"run_b","message":{{"id":"01J0000000000000000000000B"}},"payload":{{"ratio":0.5,"id":18446744073709551615,"count":-3}}}}]}}"#
    );
    let (status, body) = deliver_body("worker", body.into_bytes()).await;
    assert_eq!(
        (status, body.as_str()),
        (
            200,
            r#"["2 9007199254740993 2","0.5 18446744073709551615 -3"]"#
        )
    );
}

static STARTS: AtomicUsize = AtomicUsize::new(0);

async fn start_flaky() -> Result<(), ocel::RunError> {
    match STARTS.fetch_add(1, Ordering::SeqCst) {
        0 => Err(ocel::RunError::new("the model is not downloaded")),
        _ => Ok(()),
    }
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Flaky {
    #[ocel(on_start = start_flaky)]
    flaky: ocel::Worker,
}

#[ocel::task(worker = "flaky")]
async fn on_flaky(_image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[tokio::test]
async fn a_worker_starts_once_and_a_failed_start_fails_the_delivery_and_is_retried() {
    let body = build_envelope("on-flaky", "on-flaky", json!({ "url": "a" }), (1, 3));
    let (status, text) = deliver_body("flaky", body.clone()).await;
    assert_eq!(
        (status, text.as_str()),
        (
            500,
            "worker 'flaky' failed to start: the model is not downloaded"
        )
    );
    assert_eq!(deliver_body("flaky", body.clone()).await.0, 200);
    assert_eq!(deliver_body("flaky", body).await.0, 200);
    assert_eq!(STARTS.load(Ordering::SeqCst), 2);
}

static RUNNING: AtomicUsize = AtomicUsize::new(0);
static MOST_RUNNING: AtomicUsize = AtomicUsize::new(0);

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Single {
    #[ocel(concurrency = 2)]
    pair: ocel::Worker,
}

#[ocel::task(worker = "pair")]
async fn capped(_image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    let now = RUNNING.fetch_add(1, Ordering::SeqCst) + 1;
    MOST_RUNNING.fetch_max(now, Ordering::SeqCst);
    tokio::time::sleep(Duration::from_millis(20)).await;
    RUNNING.fetch_sub(1, Ordering::SeqCst);
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_worker_runs_no_more_deliveries_at_once_than_its_concurrency() {
    let deliveries: Vec<_> = (0..6)
        .map(|_| {
            tokio::spawn(deliver_body(
                "pair",
                build_envelope("capped", "capped", json!({ "url": "a" }), (1, 1)),
            ))
        })
        .collect();
    for delivery in deliveries {
        assert_eq!(delivery.await.expect("a delivery").0, 200);
    }
    assert_eq!(MOST_RUNNING.load(Ordering::SeqCst), 2);
}

static CANCELED: Mutex<Vec<String>> = Mutex::new(Vec::new());
static STARTED: tokio::sync::Notify = tokio::sync::Notify::const_new();
static FINISHED: tokio::sync::Notify = tokio::sync::Notify::const_new();
static CANCEL_RAN: tokio::sync::Notify = tokio::sync::Notify::const_new();

async fn record_cancel(image: &Image, run: &ocel::Run) -> Result<(), ocel::RunError> {
    record(
        &CANCELED,
        format!("on_cancel {} {}", image.url, run.is_cancelled()),
    );
    CANCEL_RAN.notify_one();
    Ok(())
}

#[ocel::task(on_cancel = record_cancel)]
async fn long_running(_image: Image, run: &ocel::Run) -> Result<(), ocel::RunError> {
    STARTED.notify_one();
    run.cancelled().await;
    record(&CANCELED, "the run saw the cancel");
    FINISHED.notify_one();
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_delivery_dropped_before_it_answers_cancels_the_run_and_runs_on_cancel() {
    let delivery = tokio::spawn(deliver_body(
        "worker",
        build_envelope(
            "long-running",
            "long-running",
            json!({ "url": "a" }),
            (1, 1),
        ),
    ));
    STARTED.notified().await;
    delivery.abort();
    tokio::time::timeout(Duration::from_secs(5), async {
        tokio::join!(FINISHED.notified(), CANCEL_RAN.notified())
    })
    .await
    .expect("the run and on_cancel finish");
    let log = take_log(&CANCELED);
    assert!(log.contains(&"on_cancel a true".to_string()), "{log:?}");
    assert!(
        log.contains(&"the run saw the cancel".to_string()),
        "{log:?}"
    );
}

#[tokio::test]
async fn an_envelope_of_many_messages_for_a_registration_without_batch_answers_400_and_runs_nothing(
) {
    let body = serde_json::to_vec(&json!({
        "v": 1,
        "topic": "measure",
        "consumer": "measure",
        "messages": [
            { "execution": "run_a", "message": { "id": "01J0000000000000000000000A" }, "attempt": { "number": 1, "of": 3 }, "payload": { "url": "a" } },
        ],
    }))
    .expect("an envelope");
    let (status, body) = deliver_body("worker", body).await;
    assert_eq!(
        (status, body.as_str()),
        (
            400,
            "'measure' of topic 'measure' takes one message at a time, and the envelope carries a batch"
        )
    );
}

#[tokio::test]
async fn a_single_message_envelope_reaches_a_batch_registration_as_a_batch_of_one() {
    let (status, body) = deliver_body(
        "worker",
        build_envelope("digest", "digest", json!({ "url": "a" }), (1, 1)),
    )
    .await;
    assert_eq!((status, body.as_str()), (200, r#"["a"]"#));
}

static ENDED: Mutex<Vec<String>> = Mutex::new(Vec::new());
static ENDED_STARTED: tokio::sync::Notify = tokio::sync::Notify::const_new();

async fn ended_on_cancel(image: &Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    record(&ENDED, format!("on_cancel {}", image.url));
    Ok(())
}

async fn ended_on_success(
    image: &Image,
    _output: &(),
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(&ENDED, format!("on_success {}", image.url));
    Ok(())
}

async fn ended_on_failure(
    image: &Image,
    _error: &ocel::RunError,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(&ENDED, format!("on_failure {}", image.url));
    Ok(())
}

async fn ended_on_complete(
    image: &Image,
    _result: Result<&(), &ocel::RunError>,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    record(&ENDED, format!("on_complete {}", image.url));
    Ok(())
}

#[ocel::task(
    on_cancel = ended_on_cancel,
    on_success = ended_on_success,
    on_failure = ended_on_failure,
    on_complete = ended_on_complete
)]
async fn ends_after_cancel(image: Image, run: &ocel::Run) -> Result<(), ocel::RunError> {
    ENDED_STARTED.notify_one();
    run.cancelled().await;
    record(&ENDED, format!("returned {}", image.url));
    match image.url.as_str() {
        "abort" => Err(ocel::RunError::abort("the image is gone")),
        _ => Ok(()),
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_cancelled_attempt_runs_on_cancel_and_none_of_the_success_failure_or_complete_hooks() {
    for url in ["ok", "abort"] {
        let delivery = tokio::spawn(deliver_body(
            "worker",
            build_envelope(
                "ends-after-cancel",
                "ends-after-cancel",
                json!({ "url": url }),
                (1, 1),
            ),
        ));
        ENDED_STARTED.notified().await;
        delivery.abort();
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert_eq!(
            take_log(&ENDED),
            [format!("on_cancel {url}"), format!("returned {url}")]
        );
    }
}

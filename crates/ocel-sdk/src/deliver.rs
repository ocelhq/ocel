use crate::declare::{collect_declarations, DeclaredConfig, DeliveryFn};
use crate::json::{convert_timestamp, convert_value};
use crate::proto::app::topic::v1::Envelope;
use crate::run::{Attempt, BoxFuture, Message, Next, Run, RunError, RunKind};
use crate::worker::{OnStart, WorkerMiddleware};
use futures_util::future::{select, Either};
use serde::de::DeserializeOwned;
use serde::Serialize;
use std::collections::HashMap;
use std::pin::pin;
use std::sync::{Arc, OnceLock};
use tokio::sync::{OnceCell, OwnedSemaphorePermit, Semaphore};
use tokio_util::sync::CancellationToken;

const DEFAULT_WORKER: &str = "worker";

#[doc(hidden)]
pub struct Delivery {
    run: Run,
    payload: serde_json::Value,
    middleware: Option<WorkerMiddleware>,
}

#[doc(hidden)]
pub enum Outcome {
    Output(Vec<u8>),
    Abort(String),
    Retry(String),
}

type StepFuture<'a, T> = BoxFuture<'a, Result<T, RunError>>;
type RunStep<P, R> = for<'a> fn(P, &'a Run) -> StepFuture<'a, R>;
type PayloadHook<P> = for<'a> fn(&'a P, &'a Run) -> StepFuture<'a, ()>;
type MiddlewareStep<P, R> = for<'a> fn(&'a P, &'a Run, Next<'a, R>) -> StepFuture<'a, R>;
type CatchErrorStep<P> = for<'a> fn(&'a P, RunError, &'a Run) -> BoxFuture<'a, RunError>;
type SuccessHook<P, R> = for<'a> fn(&'a P, &'a R, &'a Run) -> StepFuture<'a, ()>;
type FailureHook<P> = for<'a> fn(&'a P, &'a RunError, &'a Run) -> StepFuture<'a, ()>;
type CompleteHook<P, R> =
    for<'a> fn(&'a P, Result<&'a R, &'a RunError>, &'a Run) -> StepFuture<'a, ()>;

#[doc(hidden)]
pub struct Steps<P: 'static, R: 'static> {
    pub run: RunStep<P, R>,
    pub clone_payload: Option<fn(&P) -> P>,
    pub on_start_attempt: Option<PayloadHook<P>>,
    pub middleware: Option<MiddlewareStep<P, R>>,
    pub catch_error: Option<CatchErrorStep<P>>,
    pub on_success: Option<SuccessHook<P, R>>,
    pub on_failure: Option<FailureHook<P>>,
    pub on_complete: Option<CompleteHook<P, R>>,
    pub on_cancel: Option<PayloadHook<P>>,
}

impl<P: 'static, R: 'static> Steps<P, R> {
    #[doc(hidden)]
    pub const fn new(run: RunStep<P, R>) -> Self {
        Self {
            run,
            clone_payload: None,
            on_start_attempt: None,
            middleware: None,
            catch_error: None,
            on_success: None,
            on_failure: None,
            on_complete: None,
            on_cancel: None,
        }
    }
}

struct AttemptEnd {
    outcome: Outcome,
    cancelled: bool,
}

#[doc(hidden)]
pub fn run_attempt<P, R>(
    steps: &'static Steps<P, R>,
    delivery: Delivery,
) -> BoxFuture<'static, Outcome>
where
    P: DeserializeOwned + Send + Sync + 'static,
    R: Serialize + Send + Sync + 'static,
{
    Box::pin(async move {
        let Delivery {
            run,
            payload,
            middleware,
        } = delivery;
        let payload = match P::deserialize(&payload) {
            Ok(payload) => payload,
            Err(err) => return Outcome::Abort(describe_payload_mismatch(&err)),
        };
        let (owned, kept) = match steps.clone_payload {
            Some(clone_payload) => (clone_payload(&payload), Some(payload)),
            None => (payload, None),
        };
        let kept = kept.as_ref();
        let work = pin!(run_steps(steps, &run, owned, kept, middleware));
        let cancelled = pin!(run.cancelled());
        let on_cancel = async {
            if let (Some(on_cancel), Some(payload)) = (steps.on_cancel, kept) {
                report_hook_failure(&run, "on_cancel", on_cancel(payload, &run).await);
            }
        };
        match select(cancelled, work).await {
            Either::Right((ended, _)) => {
                if ended.cancelled {
                    on_cancel.await;
                }
                ended.outcome
            }
            Either::Left(((), work)) => {
                on_cancel.await;
                work.await.outcome
            }
        }
    })
}

async fn run_steps<P, R>(
    steps: &'static Steps<P, R>,
    run: &Run,
    owned: P,
    kept: Option<&P>,
    middleware: Option<WorkerMiddleware>,
) -> AttemptEnd
where
    P: Send + Sync + 'static,
    R: Serialize + Send + Sync + 'static,
{
    let mut output: Option<R> = None;
    let inner = async {
        if let (Some(on_start_attempt), Some(payload)) = (steps.on_start_attempt, kept) {
            on_start_attempt(payload, run).await?;
        }
        let ran = (steps.run)(owned, run);
        let produced = match (steps.middleware, kept) {
            (Some(middleware), Some(payload)) => middleware(payload, run, Next::new(ran)).await?,
            _ => ran.await?,
        };
        output = Some(produced);
        Ok(())
    };
    let result = match middleware {
        Some(middleware) => middleware(run, Next::new(inner)).await,
        None => inner.await,
    };
    let encoded = result.and_then(|()| match &output {
        Some(output) => serde_json::to_vec(output)
            .map_err(|err| RunError::abort(format!("the output does not encode as JSON: {err}"))),
        None => Ok(b"null".to_vec()),
    });
    let cancelled = run.is_cancelled();
    let outcome = match encoded {
        Ok(body) => {
            if let (Some(output), Some(payload), false) = (&output, kept, cancelled) {
                if let Some(on_success) = steps.on_success {
                    report_hook_failure(run, "on_success", on_success(payload, output, run).await);
                }
                if let Some(on_complete) = steps.on_complete {
                    report_hook_failure(
                        run,
                        "on_complete",
                        on_complete(payload, Ok(output), run).await,
                    );
                }
            }
            Outcome::Output(body)
        }
        Err(err) => {
            let err = match (err.is_abort(), steps.catch_error, kept) {
                (false, Some(catch_error), Some(payload)) => catch_error(payload, err, run).await,
                _ => err,
            };
            let ends_run = err.is_abort() || run.attempt.is_last();
            if let (Some(payload), true, false) = (kept, ends_run, cancelled) {
                if let Some(on_failure) = steps.on_failure {
                    report_hook_failure(run, "on_failure", on_failure(payload, &err, run).await);
                }
                if let Some(on_complete) = steps.on_complete {
                    report_hook_failure(
                        run,
                        "on_complete",
                        on_complete(payload, Err(&err), run).await,
                    );
                }
            }
            match err.is_abort() {
                true => Outcome::Abort(err.message().to_string()),
                false => Outcome::Retry(err.message().to_string()),
            }
        }
    };
    AttemptEnd { outcome, cancelled }
}

fn describe_payload_mismatch(err: &serde_json::Error) -> String {
    format!("the payload does not match the declared type: {err}")
}

fn report_hook_failure(run: &Run, hook: &str, result: Result<(), RunError>) {
    if let Err(err) = result {
        let kind = match run.kind {
            RunKind::Task => "task",
            RunKind::Consumer => "consumer",
        };
        eprintln!(
            "ocel: {kind} '{}' run {}: {hook} failed: {err}",
            run.name, run.id
        );
    }
}

struct Registration {
    kind: RunKind,
    name: &'static str,
    worker: &'static str,
    batch: bool,
    deliver: DeliveryFn,
}

#[derive(Default)]
struct WorkerSetup {
    slots: Option<Arc<Semaphore>>,
    on_start: Option<OnStart>,
    started: OnceCell<()>,
    middleware: Option<WorkerMiddleware>,
}

impl WorkerSetup {
    async fn ensure_started(&self) -> Result<(), RunError> {
        let Some(on_start) = self.on_start else {
            return Ok(());
        };
        self.started.get_or_try_init(on_start).await.map(|_| ())
    }

    async fn acquire_slot(&self) -> Option<OwnedSemaphorePermit> {
        let slots = self.slots.clone()?;
        slots.acquire_owned().await.ok()
    }
}

#[derive(Default)]
struct Registry {
    registrations: HashMap<(String, String), Registration>,
    workers: HashMap<String, WorkerSetup>,
    undeclared_worker: WorkerSetup,
}

impl Registry {
    fn new() -> Result<Self, String> {
        let declared = collect_declarations().map_err(|err| err.to_string())?;
        let mut registry = Self::default();
        for resource in declared.resources {
            match resource.config {
                DeclaredConfig::Task {
                    worker,
                    batch,
                    deliver,
                    ..
                } => {
                    registry.registrations.insert(
                        (resource.name.to_string(), resource.name.to_string()),
                        Registration {
                            kind: RunKind::Task,
                            name: resource.name,
                            worker: resolve_worker_name(worker),
                            batch: batch.is_some(),
                            deliver,
                        },
                    );
                }
                DeclaredConfig::Consumer {
                    topic,
                    worker,
                    batch,
                    deliver,
                    ..
                } => {
                    registry.registrations.insert(
                        (topic.to_string(), resource.name.to_string()),
                        Registration {
                            kind: RunKind::Consumer,
                            name: resource.name,
                            worker: resolve_worker_name(worker),
                            batch: batch.is_some(),
                            deliver,
                        },
                    );
                }
                DeclaredConfig::Worker {
                    concurrency,
                    on_start,
                    middleware,
                } => {
                    registry.workers.insert(
                        resource.name.to_string(),
                        WorkerSetup {
                            slots: (concurrency > 0)
                                .then(|| Arc::new(Semaphore::new(concurrency as usize))),
                            on_start,
                            started: OnceCell::new(),
                            middleware,
                        },
                    );
                }
                _ => {}
            }
        }
        Ok(registry)
    }

    fn find_worker(&self, name: &str) -> &WorkerSetup {
        self.workers.get(name).unwrap_or(&self.undeclared_worker)
    }
}

fn resolve_worker_name(worker: &'static str) -> &'static str {
    match worker {
        "" => DEFAULT_WORKER,
        named => named,
    }
}

fn ensure_registry() -> &'static Result<Registry, String> {
    static REGISTRY: OnceLock<Result<Registry, String>> = OnceLock::new();
    REGISTRY.get_or_init(Registry::new)
}

/// Serve one delivery to the worker named `worker`: `body` is the envelope the queue
/// POSTed, and the answer is the HTTP status and body to reply with. The generated worker
/// entry calls it, and it runs on that entry's tokio runtime. A delivery whose future is
/// dropped before it answers cancels the run, which the handler observes through
/// [`Run::cancelled`].
#[doc(hidden)]
pub async fn deliver(worker: &str, body: &[u8]) -> (u16, Vec<u8>) {
    let envelope: Envelope = match serde_json::from_slice(body) {
        Ok(envelope) => envelope,
        Err(err) => return answer_text(400, format!("the body is not a delivery envelope: {err}")),
    };
    let registry = match ensure_registry() {
        Ok(registry) => registry,
        Err(err) => return answer_text(500, err.clone()),
    };
    let key = (envelope.topic.clone(), envelope.consumer.clone());
    let Some(registration) = registry.registrations.get(&key) else {
        return answer_text(
            404,
            format!(
                "this binary registers no consumer '{}' of topic '{}'",
                key.1, key.0
            ),
        );
    };
    if registration.worker != worker {
        return answer_text(
            404,
            format!(
                "consumer '{}' of topic '{}' runs on worker '{}', and this is worker '{worker}'",
                key.1, key.0, registration.worker
            ),
        );
    }
    if !registration.batch && !envelope.messages.is_empty() {
        return answer_text(
            400,
            format!(
                "'{}' of topic '{}' takes one message at a time, and the envelope carries a batch",
                key.1, key.0
            ),
        );
    }
    let setup = registry.find_worker(worker);
    if let Err(err) = setup.ensure_started().await {
        return answer_text(
            500,
            format!("worker '{worker}' failed to start: {}", err.message()),
        );
    }
    let slot = setup.acquire_slot().await;

    let (run, payload) = read_envelope(registration, envelope);
    let cancellation = run.cancellation.clone();
    let delivery = Delivery {
        run,
        payload,
        middleware: setup.middleware,
    };
    let deliver = registration.deliver;
    let guard = cancellation.drop_guard();
    let joined = tokio::spawn(async move {
        let _slot = slot;
        deliver(delivery).await
    })
    .await;
    guard.disarm();

    match joined {
        Ok(Outcome::Output(body)) => (200, body),
        Ok(Outcome::Abort(reason)) => (
            422,
            serde_json::to_vec(&serde_json::json!({ "abort": { "reason": reason } }))
                .unwrap_or_default(),
        ),
        Ok(Outcome::Retry(message)) => answer_text(500, message),
        Err(err) => answer_text(500, format!("the run did not finish: {err}")),
    }
}

fn read_envelope(registration: &Registration, envelope: Envelope) -> (Run, serde_json::Value) {
    let first = envelope.messages.first();
    let (id, message) = match first {
        Some(delivery) => (
            delivery.execution.clone(),
            delivery.message.as_option().cloned(),
        ),
        None => (
            envelope.execution.clone(),
            envelope.message.as_option().cloned(),
        ),
    };
    let attempt = first
        .and_then(|delivery| delivery.attempt.as_option())
        .or(envelope.attempt.as_option())
        .cloned()
        .unwrap_or_default();
    let run = Run {
        kind: registration.kind,
        name: registration.name.to_string(),
        topic: envelope.topic.clone(),
        id,
        attempt: Attempt {
            number: attempt.number.max(1) as u32,
            of: attempt.of.max(1) as u32,
            first_attempted_at: attempt
                .first_attempted_at
                .as_option()
                .and_then(convert_timestamp),
        },
        message: Message {
            id: message
                .as_ref()
                .map(|one| one.id.clone())
                .unwrap_or_default(),
            published_at: message
                .as_ref()
                .and_then(|one| one.published_at.as_option())
                .and_then(convert_timestamp),
        },
        cancellation: CancellationToken::new(),
    };
    let payload = match (registration.batch, envelope.messages.is_empty()) {
        (true, false) => serde_json::Value::Array(
            envelope
                .messages
                .iter()
                .map(|delivery| convert_value(delivery.payload.as_option()))
                .collect(),
        ),
        (true, true) => serde_json::Value::Array(vec![convert_value(envelope.payload.as_option())]),
        (false, _) => convert_value(envelope.payload.as_option()),
    };
    (run, payload)
}

fn answer_text(status: u16, message: String) -> (u16, Vec<u8>) {
    (status, message.into_bytes())
}

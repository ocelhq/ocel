use crate::binding;
use crate::declare::is_discovering;
use crate::payload::{encode_payload, Due};
use crate::proto::app::task::v1::{
    BatchTriggerItem, BatchTriggerRequest, Debounce, TaskServiceClient, TriggerOptions,
    TriggerRequest,
};
use crate::runtime::read_client_config;
use crate::{Error, Lane};
use connectrpc::client::HttpClient;
use serde::Serialize;
use std::future::{Future, IntoFuture};
use std::marker::PhantomData;
use std::pin::Pin;
use std::sync::OnceLock;
use std::time::{Duration, SystemTime};

type ResultFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;

struct Connection {
    client: TaskServiceClient<HttpClient>,
}

/// A run a trigger started. [`crate::runs`] reads and acts on it by its id.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct RunHandle {
    /// The run's id.
    pub id: String,
}

/// A task: a function a worker runs once per trigger, retried by its policy. The
/// [`macro@crate::task`] attribute declares one and replaces the function with this handle,
/// which triggers it with a payload of type `P`; `R` is what a run returns.
pub struct Task<P, R> {
    name: &'static str,
    connection: OnceLock<Connection>,
    types: PhantomData<fn(P) -> R>,
}

impl<P, R> Task<P, R> {
    #[doc(hidden)]
    pub const fn new(name: &'static str) -> Self {
        Self {
            name,
            connection: OnceLock::new(),
            types: PhantomData,
        }
    }

    /// The name the task was declared under.
    pub fn name(&self) -> &str {
        self.name
    }

    fn ensure_connection(&self, access: &str) -> Result<&Connection, Error> {
        if is_discovering() {
            return Err(Error::Unprovisioned {
                resource: self.describe_resource(),
                access: access.to_string(),
            });
        }
        if let Some(connection) = self.connection.get() {
            return Ok(connection);
        }
        binding::refuse_unbound_task(self.name)?;
        let connection = Connection {
            client: TaskServiceClient::new(HttpClient::plaintext(), read_client_config()?),
        };
        Ok(self.connection.get_or_init(|| connection))
    }

    fn describe_resource(&self) -> String {
        format!("task(\"{}\")", self.name)
    }

    fn refuse_access(&self, access: &str, err: &connectrpc::ConnectError) -> Error {
        Error::RuntimeRefused {
            resource: self.describe_resource(),
            access: access.to_string(),
            said: err.to_string(),
        }
    }
}

impl<P: Serialize, R> Task<P, R> {
    /// Start a run with `payload`. The returned builder sets the run's options, and awaiting
    /// it triggers the run and answers with its [`RunHandle`].
    pub fn trigger(&self, payload: P) -> Trigger<'_, P, R> {
        Trigger {
            task: self,
            payload: encode_payload(&payload),
            options: Options::default(),
        }
    }

    /// Start one run per trigger in `triggers`, each built with [`Task::trigger`] on this
    /// task, and answer with their [`RunHandle`]s in the same order. A batch holds at most
    /// 1,000 triggers.
    pub async fn batch_trigger<'a>(
        &'a self,
        triggers: impl IntoIterator<Item = Trigger<'a, P, R>>,
    ) -> Result<Vec<RunHandle>, Error> {
        let connection = self.ensure_connection("batch_trigger")?;
        let mut items = Vec::new();
        for trigger in triggers {
            if !std::ptr::eq(trigger.task, self) {
                return Err(Error::MixedBatch {
                    task: self.name.to_string(),
                    other: trigger.task.name.to_string(),
                });
            }
            items.push(BatchTriggerItem {
                payload: trigger.payload?,
                options: trigger.options.into_wire().into(),
                ..Default::default()
            });
        }
        let response = connection
            .client
            .batch_trigger(BatchTriggerRequest {
                task: self.name.to_string(),
                items,
                ..Default::default()
            })
            .await
            .map_err(|err| self.refuse_access("batch_trigger", &err))?;
        Ok(response
            .into_owned()
            .ids
            .into_iter()
            .map(|id| RunHandle { id })
            .collect())
    }
}

#[derive(Default)]
struct Options {
    due: Option<Due>,
    ttl: Option<Duration>,
    idempotency_key: String,
    idempotency_key_ttl: Option<Duration>,
    debounce: Option<(String, Duration)>,
    key: String,
    lane: Option<Lane>,
    max_attempts: i32,
    tags: Vec<String>,
    metadata: Option<serde_json::Map<String, serde_json::Value>>,
}

impl Options {
    fn into_wire(self) -> TriggerOptions {
        TriggerOptions {
            due_at: self.due.map(Due::to_timestamp).into(),
            ttl: self.ttl.map(Into::into).into(),
            idempotency_key: self.idempotency_key,
            idempotency_key_ttl: self.idempotency_key_ttl.map(Into::into).into(),
            debounce: self
                .debounce
                .map(|(key, delay)| Debounce {
                    key,
                    delay: Some(delay.into()).into(),
                    ..Default::default()
                })
                .into(),
            key: self.key,
            lane: self.lane.map(Lane::to_wire).unwrap_or_default().into(),
            max_attempts: self.max_attempts,
            tags: self.tags,
            metadata: self
                .metadata
                .map(|metadata| serde_json::Value::Object(metadata).to_string().into_bytes())
                .unwrap_or_default(),
            ..Default::default()
        }
    }
}

/// The run [`Task::trigger`] starts, which is triggered by awaiting it.
pub struct Trigger<'a, P, R> {
    task: &'a Task<P, R>,
    payload: Result<Vec<u8>, Error>,
    options: Options,
}

impl<P, R> Trigger<'_, P, R> {
    /// Hold the run until `delay` from now, at most 30 days.
    pub fn delay(mut self, delay: Duration) -> Self {
        self.options.due = Some(Due::In(delay));
        self
    }

    /// Hold the run until `at`, at most 30 days from now.
    pub fn at(mut self, at: SystemTime) -> Self {
        self.options.due = Some(Due::At(at));
        self
    }

    /// Expire the run if it has not started within `ttl` of being due, at most 14 days.
    pub fn ttl(mut self, ttl: Duration) -> Self {
        self.options.ttl = Some(ttl);
        self
    }

    /// Answer a trigger that repeats `key` with the run the first one started, instead of
    /// starting another.
    pub fn idempotency_key(mut self, key: impl Into<String>) -> Self {
        self.options.idempotency_key = key.into();
        self
    }

    /// How long the idempotency key keeps answering with its run. Left out, it is 30 days.
    pub fn idempotency_key_ttl(mut self, ttl: Duration) -> Self {
        self.options.idempotency_key_ttl = Some(ttl);
        self
    }

    /// Fold triggers sharing `key` that arrive within `delay` of each other into one run,
    /// which starts `delay` after the last of them with its payload.
    pub fn debounce(mut self, key: impl Into<String>, delay: Duration) -> Self {
        self.options.debounce = Some((key.into(), delay));
        self
    }

    /// On an ordered task, the key runs are ordered by: runs sharing a key start one at a
    /// time, in the order they were triggered.
    pub fn key(mut self, key: impl Into<String>) -> Self {
        self.options.key = key.into();
        self
    }

    /// The lane the run waits in.
    pub fn lane(mut self, lane: Lane) -> Self {
        self.options.lane = Some(lane);
        self
    }

    /// Allow the run fewer attempts than the task's retry policy does.
    pub fn max_attempts(mut self, attempts: u32) -> Self {
        self.options.max_attempts = attempts.min(i32::MAX as u32) as i32;
        self
    }

    /// Tags the run is listed by.
    pub fn tags(mut self, tags: impl IntoIterator<Item = impl Into<String>>) -> Self {
        self.options.tags = tags.into_iter().map(Into::into).collect();
        self
    }

    /// Fixed metadata kept on the run's record.
    pub fn metadata(mut self, metadata: serde_json::Map<String, serde_json::Value>) -> Self {
        self.options.metadata = Some(metadata);
        self
    }
}

impl<'a, P, R> IntoFuture for Trigger<'a, P, R> {
    type Output = Result<RunHandle, Error>;
    type IntoFuture = ResultFuture<'a, RunHandle>;

    fn into_future(self) -> Self::IntoFuture {
        let Trigger {
            task,
            payload,
            options,
        } = self;
        let request = payload.map(|payload| TriggerRequest {
            payload,
            options: options.into_wire().into(),
            ..Default::default()
        });
        let connection = task.ensure_connection("trigger");
        let resource = task.describe_resource();
        let name = task.name.to_string();
        Box::pin(async move {
            let connection = connection?;
            let mut request = request?;
            request.task = name;
            let response =
                connection
                    .client
                    .trigger(request)
                    .await
                    .map_err(|err| Error::RuntimeRefused {
                        resource,
                        access: "trigger".to_string(),
                        said: err.to_string(),
                    })?;
            Ok(RunHandle {
                id: response.into_owned().id,
            })
        })
    }
}

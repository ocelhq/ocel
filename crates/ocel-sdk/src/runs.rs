//! The runs tasks were triggered into: read them, list them, and cancel, replay or
//! reschedule one by its id.

use crate::declare::is_discovering;
use crate::json::{convert_timestamp, read_json};
use crate::payload::Due;
use crate::proto::app::task::v1::{
    CancelRunRequest, ListRunsRequest, ReplayRunRequest, RescheduleRunRequest, RetrieveRunRequest,
    Run as WireRun, RunStatus as WireRunStatus, TaskServiceClient,
};
use crate::runtime::read_client_config;
use crate::{Error, RunHandle};
use connectrpc::client::HttpClient;
use std::future::{Future, IntoFuture};
use std::pin::Pin;
use std::sync::OnceLock;
use std::time::{Duration, SystemTime};

type ResultFuture<T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send>>;

/// Where a run is in its life.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum RunStatus {
    /// Triggered with a delay that has not passed.
    Delayed,
    /// Due, and waiting for a worker.
    Queued,
    /// An attempt is running.
    Executing,
    /// An attempt succeeded.
    Completed,
    /// The last attempt failed, or one aborted.
    Failed,
    /// Canceled before it finished.
    Canceled,
    /// Not started before its time to live ran out.
    Expired,
    /// An attempt ran past the task's maximum duration.
    TimedOut,
}

impl RunStatus {
    fn to_wire(self) -> WireRunStatus {
        match self {
            Self::Delayed => WireRunStatus::RUN_STATUS_DELAYED,
            Self::Queued => WireRunStatus::RUN_STATUS_QUEUED,
            Self::Executing => WireRunStatus::RUN_STATUS_EXECUTING,
            Self::Completed => WireRunStatus::RUN_STATUS_COMPLETED,
            Self::Failed => WireRunStatus::RUN_STATUS_FAILED,
            Self::Canceled => WireRunStatus::RUN_STATUS_CANCELED,
            Self::Expired => WireRunStatus::RUN_STATUS_EXPIRED,
            Self::TimedOut => WireRunStatus::RUN_STATUS_TIMED_OUT,
        }
    }

    fn from_wire(status: WireRunStatus) -> Option<Self> {
        match status {
            WireRunStatus::RUN_STATUS_DELAYED => Some(Self::Delayed),
            WireRunStatus::RUN_STATUS_QUEUED => Some(Self::Queued),
            WireRunStatus::RUN_STATUS_EXECUTING => Some(Self::Executing),
            WireRunStatus::RUN_STATUS_COMPLETED => Some(Self::Completed),
            WireRunStatus::RUN_STATUS_FAILED => Some(Self::Failed),
            WireRunStatus::RUN_STATUS_CANCELED => Some(Self::Canceled),
            WireRunStatus::RUN_STATUS_EXPIRED => Some(Self::Expired),
            WireRunStatus::RUN_STATUS_TIMED_OUT => Some(Self::TimedOut),
            WireRunStatus::RUN_STATUS_UNSPECIFIED => None,
        }
    }
}

/// The record a run keeps from its trigger to its end.
#[derive(Clone, Debug, PartialEq)]
pub struct RunRecord {
    /// The run's id, as the trigger answered it.
    pub id: String,
    /// The name the run's task was declared under.
    pub task: String,
    /// Where the run is in its life, or `None` for a status this SDK does not know.
    pub status: Option<RunStatus>,
    /// The payload the run was triggered with, exactly as it was encoded: integers stay
    /// integers at full 64-bit precision and floats stay floats. `null` when the run has no
    /// payload.
    pub payload: serde_json::Value,
    /// What the run returned, exactly as the run encoded it, or `null` until it completes.
    pub output: serde_json::Value,
    /// The error the last failed attempt returned.
    pub error: String,
    /// How many attempts have started.
    pub attempts: u32,
    /// The tags the run was triggered with.
    pub tags: Vec<String>,
    /// The metadata the run was triggered with, empty when it was triggered with none.
    pub metadata: serde_json::Map<String, serde_json::Value>,
    /// When the run was triggered.
    pub created_at: Option<SystemTime>,
    /// When the run is or was due.
    pub due_at: Option<SystemTime>,
    /// When the run's first attempt started.
    pub started_at: Option<SystemTime>,
    /// When the run ended.
    pub finished_at: Option<SystemTime>,
    /// When the run expires if it has not started.
    pub expires_at: Option<SystemTime>,
}

/// One page of runs.
#[derive(Clone, Debug, PartialEq)]
pub struct RunPage {
    /// The runs on this page.
    pub runs: Vec<RunRecord>,
    /// The cursor the next page starts at, or `None` on the last page.
    pub next_cursor: Option<String>,
}

fn ensure_client(access: &str) -> Result<&'static TaskServiceClient<HttpClient>, Error> {
    static CLIENT: OnceLock<TaskServiceClient<HttpClient>> = OnceLock::new();
    if is_discovering() {
        return Err(Error::Unprovisioned {
            resource: "runs".to_string(),
            access: access.to_string(),
        });
    }
    if let Some(client) = CLIENT.get() {
        return Ok(client);
    }
    let opened = TaskServiceClient::new(HttpClient::plaintext(), read_client_config()?);
    Ok(CLIENT.get_or_init(|| opened))
}

fn refuse_access(id: &str, access: &str, err: connectrpc::ConnectError) -> Error {
    if err.code == connectrpc::ErrorCode::NotFound {
        return Error::UnknownRun { id: id.to_string() };
    }
    Error::RuntimeRefused {
        resource: "runs".to_string(),
        access: access.to_string(),
        said: err.to_string(),
    }
}

fn convert_run(run: Option<&WireRun>, access: &str) -> Result<RunRecord, Error> {
    let run = run.cloned().unwrap_or_default();
    let refuse_json = |field: &str, err: serde_json::Error| Error::RuntimeRefused {
        resource: "runs".to_string(),
        access: access.to_string(),
        said: format!("run {} has a {field} that is not JSON: {err}", run.id),
    };
    let payload = read_json(&run.payload).map_err(|err| refuse_json("payload", err))?;
    let output = read_json(&run.output).map_err(|err| refuse_json("output", err))?;
    let metadata = read_json(&run.metadata).map_err(|err| refuse_json("metadata", err))?;
    Ok(RunRecord {
        status: run.status.as_known().and_then(RunStatus::from_wire),
        payload,
        output,
        attempts: run.attempts.max(0) as u32,
        created_at: run.created_at.as_option().and_then(convert_timestamp),
        due_at: run.due_at.as_option().and_then(convert_timestamp),
        started_at: run.started_at.as_option().and_then(convert_timestamp),
        finished_at: run.finished_at.as_option().and_then(convert_timestamp),
        expires_at: run.expires_at.as_option().and_then(convert_timestamp),
        metadata,
        id: run.id,
        task: run.task,
        error: run.error,
        tags: run.tags,
    })
}

/// Read the run with `id`. It fails with [`Error::UnknownRun`] when no run has it.
pub async fn retrieve(id: &str) -> Result<RunRecord, Error> {
    let response = ensure_client("retrieve")?
        .retrieve_run(RetrieveRunRequest {
            id: id.to_string(),
            ..Default::default()
        })
        .await
        .map_err(|err| refuse_access(id, "retrieve", err))?
        .into_owned();
    convert_run(response.run.as_option(), "retrieve")
}

/// Cancel the run with `id`: no attempt starts after this, and a running attempt is told
/// through [`Run::cancelled`](crate::Run::cancelled). Answers with the run as it is now.
pub async fn cancel(id: &str) -> Result<RunRecord, Error> {
    let response = ensure_client("cancel")?
        .cancel_run(CancelRunRequest {
            id: id.to_string(),
            ..Default::default()
        })
        .await
        .map_err(|err| refuse_access(id, "cancel", err))?
        .into_owned();
    convert_run(response.run.as_option(), "cancel")
}

/// Trigger the run with `id` again with its payload and options, and answer with the new
/// run.
pub async fn replay(id: &str) -> Result<RunHandle, Error> {
    let response = ensure_client("replay")?
        .replay_run(ReplayRunRequest {
            id: id.to_string(),
            ..Default::default()
        })
        .await
        .map_err(|err| refuse_access(id, "replay", err))?
        .into_owned();
    Ok(RunHandle { id: response.id })
}

/// Move the delayed run with `id` to a new due time. The returned builder sets it, and
/// awaiting it reschedules the run, due now unless it says otherwise.
pub fn reschedule(id: &str) -> Reschedule {
    Reschedule {
        id: id.to_string(),
        due: Due::In(Duration::ZERO),
    }
}

/// Page through runs, newest first. The returned builder filters and bounds the page, and
/// awaiting it reads one.
pub fn list() -> RunList {
    RunList {
        task: None,
        statuses: Vec::new(),
        tags: Vec::new(),
        cursor: String::new(),
        limit: 0,
    }
}

/// The reschedule [`reschedule`] opens, which is performed by awaiting it.
pub struct Reschedule {
    id: String,
    due: Due,
}

impl Reschedule {
    /// Make the run due `delay` from now, at most 30 days.
    pub fn delay(mut self, delay: Duration) -> Self {
        self.due = Due::In(delay);
        self
    }

    /// Make the run due at `at`, at most 30 days from now.
    pub fn at(mut self, at: SystemTime) -> Self {
        self.due = Due::At(at);
        self
    }
}

impl IntoFuture for Reschedule {
    type Output = Result<RunRecord, Error>;
    type IntoFuture = ResultFuture<RunRecord>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let response = ensure_client("reschedule")?
                .reschedule_run(RescheduleRunRequest {
                    id: self.id.clone(),
                    due_at: Some(self.due.to_timestamp()).into(),
                    ..Default::default()
                })
                .await
                .map_err(|err| refuse_access(&self.id, "reschedule", err))?
                .into_owned();
            convert_run(response.run.as_option(), "reschedule")
        })
    }
}

/// The page [`list`] reads, which is read by awaiting it.
pub struct RunList {
    task: Option<String>,
    statuses: Vec<RunStatus>,
    tags: Vec<String>,
    cursor: String,
    limit: i32,
}

impl RunList {
    /// Only runs of the task declared as `name`, such as a task handle's
    /// [`name`](crate::Task::name).
    pub fn task(mut self, name: impl Into<String>) -> Self {
        self.task = Some(name.into());
        self
    }

    /// Only runs in `status`. Called more than once, runs in any of the statuses.
    pub fn status(mut self, status: RunStatus) -> Self {
        self.statuses.push(status);
        self
    }

    /// Only runs triggered with every one of `tags`.
    pub fn tags(mut self, tags: impl IntoIterator<Item = impl Into<String>>) -> Self {
        self.tags = tags.into_iter().map(Into::into).collect();
        self
    }

    /// Start at the cursor a previous page answered with.
    pub fn cursor(mut self, cursor: impl Into<String>) -> Self {
        self.cursor = cursor.into();
        self
    }

    /// Read at most `limit` runs, at most 1,000.
    pub fn limit(mut self, limit: u32) -> Self {
        self.limit = limit.min(i32::MAX as u32) as i32;
        self
    }
}

impl IntoFuture for RunList {
    type Output = Result<RunPage, Error>;
    type IntoFuture = ResultFuture<RunPage>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let client = ensure_client("list")?;
            let task = self.task.clone().unwrap_or_default();
            let response = client
                .list_runs(ListRunsRequest {
                    task,
                    statuses: self
                        .statuses
                        .iter()
                        .map(|status| status.to_wire().into())
                        .collect(),
                    tags: self.tags,
                    cursor: self.cursor,
                    limit: self.limit,
                    ..Default::default()
                })
                .await
                .map_err(|err| Error::RuntimeRefused {
                    resource: "runs".to_string(),
                    access: "list".to_string(),
                    said: err.to_string(),
                })?
                .into_owned();
            Ok(RunPage {
                runs: response
                    .runs
                    .iter()
                    .map(|run| convert_run(Some(run), "list"))
                    .collect::<Result<_, _>>()?,
                next_cursor: Some(response.next_cursor).filter(|cursor| !cursor.is_empty()),
            })
        })
    }
}

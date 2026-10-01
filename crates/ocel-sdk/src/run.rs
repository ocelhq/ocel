use std::fmt;
use std::future::{Future, IntoFuture};
use std::pin::Pin;
use std::time::SystemTime;
use tokio_util::sync::CancellationToken;

#[doc(hidden)]
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Whether a run belongs to a task or to a consumer of a topic.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RunKind {
    /// A run of a task, started by a trigger.
    Task,
    /// A run of a topic's consumer, started by a message sent to the topic.
    Consumer,
}

/// Which attempt of a run this is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attempt {
    /// The attempt's number, counted from 1.
    pub number: u32,
    /// How many attempts the run is allowed in all.
    pub of: u32,
    /// When the run's first attempt started.
    pub first_attempted_at: Option<SystemTime>,
}

impl Attempt {
    /// Whether no attempt follows this one when it fails.
    pub fn is_last(&self) -> bool {
        self.number >= self.of
    }
}

/// The message a run was started by.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Message {
    /// The message's id.
    pub id: String,
    /// When the message was sent or the task triggered.
    pub published_at: Option<SystemTime>,
}

/// What a task or consumer is told about the run it is serving, handed to it beside its
/// payload and to every hook and middleware around it.
#[derive(Clone, Debug)]
pub struct Run {
    pub(crate) kind: RunKind,
    pub(crate) name: String,
    pub(crate) topic: String,
    pub(crate) id: String,
    pub(crate) attempt: Attempt,
    pub(crate) message: Message,
    pub(crate) cancellation: CancellationToken,
}

impl Run {
    /// Whether this is a task's run or a consumer's.
    pub fn kind(&self) -> RunKind {
        self.kind
    }

    /// The name of the task, or of the consumer, the run belongs to.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The topic the run's message was sent to, which for a task is the task's name.
    pub fn topic(&self) -> &str {
        &self.topic
    }

    /// The run's id: a task's run id, or the execution of a consumer's message. A batch
    /// carries its first message's.
    pub fn id(&self) -> &str {
        &self.id
    }

    /// Which attempt this is.
    pub fn attempt(&self) -> &Attempt {
        &self.attempt
    }

    /// The message the run was started by. A batch carries its first message.
    pub fn message(&self) -> &Message {
        &self.message
    }

    /// Whether the run was canceled while this attempt was running. A canceled attempt's
    /// answer is never read, so a handler stops its work at the next point it can.
    pub fn is_cancelled(&self) -> bool {
        self.cancellation.is_cancelled()
    }

    /// Resolves once the run is canceled, and never resolves when it is not.
    pub fn cancelled(&self) -> impl Future<Output = ()> + Send + '_ {
        self.cancellation.cancelled()
    }
}

/// Why a run's attempt failed. A [`RunError::new`] error, or any error `?` converts from, is
/// retried while attempts remain; an [`RunError::abort`] error fails the run without retrying.
pub struct RunError {
    message: String,
    abort: bool,
    source: Option<Box<dyn std::error::Error + Send + Sync + 'static>>,
}

impl RunError {
    /// A failure the run is retried after, while attempts remain.
    pub fn new(message: impl fmt::Display) -> Self {
        Self {
            message: message.to_string(),
            abort: false,
            source: None,
        }
    }

    /// A failure that ends the run now, with no attempt after it.
    pub fn abort(reason: impl fmt::Display) -> Self {
        Self {
            message: reason.to_string(),
            abort: true,
            source: None,
        }
    }

    /// The same failure, ending the run now with no attempt after it.
    pub fn into_abort(mut self) -> Self {
        self.abort = true;
        self
    }

    /// Whether the failure ends the run without another attempt.
    pub fn is_abort(&self) -> bool {
        self.abort
    }

    /// What went wrong.
    pub fn message(&self) -> &str {
        &self.message
    }

    /// The error `?` converted from, if the failure came from one.
    pub fn source(&self) -> Option<&(dyn std::error::Error + Send + Sync + 'static)> {
        self.source.as_deref()
    }
}

impl<E> From<E> for RunError
where
    E: std::error::Error + Send + Sync + 'static,
{
    fn from(err: E) -> Self {
        Self {
            message: err.to_string(),
            abort: false,
            source: Some(Box::new(err)),
        }
    }
}

impl fmt::Display for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl fmt::Debug for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("RunError")
            .field("message", &self.message)
            .field("abort", &self.abort)
            .finish()
    }
}

/// The rest of an attempt that a middleware wraps. Awaiting it runs everything inside the
/// middleware, and a middleware that never awaits it skips the run.
pub struct Next<'a, T = ()> {
    future: BoxFuture<'a, Result<T, RunError>>,
}

impl<'a, T> Next<'a, T> {
    pub(crate) fn new(future: impl Future<Output = Result<T, RunError>> + Send + 'a) -> Self {
        Self {
            future: Box::pin(future),
        }
    }
}

impl<'a, T> IntoFuture for Next<'a, T> {
    type Output = Result<T, RunError>;
    type IntoFuture = BoxFuture<'a, Result<T, RunError>>;

    fn into_future(self) -> Self::IntoFuture {
        self.future
    }
}

use crate::run::{BoxFuture, Next, Run, RunError};

#[doc(hidden)]
pub type OnStart = fn() -> BoxFuture<'static, Result<(), RunError>>;

#[doc(hidden)]
pub type WorkerMiddleware = for<'a> fn(&'a Run, Next<'a>) -> BoxFuture<'a, Result<(), RunError>>;

/// The compute that serves tasks and topic consumers, joined by name to the app of the same
/// name in `ocel.json`. A field of this type in a struct deriving
/// [`Resources`](macro@crate::Resources) is the declaration; a task or consumer runs on it
/// when it names it with `worker = "<NAME>"`, and on the worker named `worker` otherwise.
#[derive(Clone, Debug)]
pub struct Worker {
    name: String,
}

impl Worker {
    /// Take the handle for the worker named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the worker as well as handing
    /// back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self { name: name.into() }
    }

    /// The name the worker was declared under.
    pub fn name(&self) -> &str {
        &self.name
    }
}

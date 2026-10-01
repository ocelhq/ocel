//! The library Rust apps import to declare the infrastructure and the environment an app
//! needs. A declaration is a struct in the app's own crate, loaded as a value:
//!
//! ```ignore
//! #[derive(ocel::Resources, Clone)]
//! pub struct Infra {
//!     #[ocel(name = "main", version = "17")]
//!     pub db: ocel::Postgres,
//!     #[ocel(name = "avatars", public)]
//!     pub avatars: ocel::Bucket,
//!     #[ocel(name = "orders", retry(max_attempts = 5))]
//!     pub orders: ocel::Topic<Order>,
//!     #[ocel(name = "media", concurrency = 4)]
//!     pub media: ocel::Worker,
//!     #[ocel(eviction = "allkeys-lru", entries = [Requests])]
//!     pub cache: ocel::Kv,
//! }
//!
//! #[derive(ocel::Env, Clone)]
//! pub struct Env {
//!     pub database_name: String,
//!     #[ocel(sensitive)]
//!     pub api_key: String,
//!     pub signing_key: ocel::Secret,
//!     #[ocel(default = 3000)]
//!     pub port: u16,
//! }
//!
//! let infra = Infra::load()?;
//! let env = Env::load()?;
//! ```
//!
//! Background work is declared on functions: [`macro@task`] turns one into a task a worker
//! runs per trigger, and [`macro@consumer`] and [`macro@batch_consumer`] make one a consumer
//! of a topic, named by the [`TopicName`] its struct holds.
//!
//! ```ignore
//! #[ocel::task(retry(max_attempts = 5), worker = "media")]
//! async fn resize_image(image: Image, run: &ocel::Run) -> Result<Resized, ocel::RunError> {
//!     resize(image).await
//! }
//!
//! #[ocel::consumer(topic = Infra::ORDERS)]
//! async fn send_receipt(order: Order, run: &ocel::Run) -> Result<(), ocel::RunError> {
//!     mail(&order).await
//! }
//!
//! let run = resize_image.trigger(image).delay(Duration::from_secs(60)).await?;
//! let record = ocel::runs::retrieve(&run.id).await?;
//! ```
//!
//! Each declaration registers at link time, so discovery reads it by running the binary
//! with `#[ocel::main]` on its `main`, without loading a value. At runtime `load` reads the
//! bindings and the values the deploy delivered.

mod binding;
mod bucket;
mod declare;
mod deliver;
mod env;
mod error;
mod json;
pub mod kv;
mod lane;
mod payload;
mod postgres;
#[doc(hidden)]
pub mod proto;
mod run;
pub mod runs;
mod runtime;
mod schema;
mod task;
mod topic;
mod worker;

pub use bucket::{
    Bucket, Get, GetResult, List, Object, Payload, Put, Reader, Sign, SignUpload, SignedUpload,
    Writer,
};
pub use declare::discover;
pub use env::{deployment_url, Secret};
pub use error::Error;
pub use kv::{Kv, KvKey, KvParameter};
pub use lane::Lane;
pub use postgres::Postgres;
pub use run::{Attempt, Message, Next, Run, RunError, RunKind};
pub use task::{RunHandle, Task, Trigger};
pub use topic::{
    DeadLetter, DeadLetterList, DeadLetterPage, DeadLetters, Topic, TopicName, TopicSend,
};
pub use worker::Worker;

/// Runs discovery before `main`, and returns from `main` once discovery is done.
pub use ocel_macros::main;

/// Declares every [`Postgres`], [`Bucket`], [`Topic`], [`Worker`] and [`Kv`] field of a struct, and
/// writes the `load` that hands the struct back with a handle in each field. For each
/// [`Topic`] field it also writes a [`TopicName`] constant named after the field in upper
/// case, which consumers name their topic by.
pub use ocel_macros::Resources;

/// Derives [`KvKey`] for a struct whose fields are the parameters of its entry's pattern,
/// checking the fields against the pattern when the app builds.
pub use ocel_macros::KvKey;

/// Declares every field of a struct as an environment variable, and writes the `load` that
/// reads the delivered values into it.
pub use ocel_macros::Env;

/// Declares every field of a struct as a member of the group named after the
/// `#[ocel(group)]` field whose type it is. A struct that no such field has as its type
/// declares nothing.
pub use ocel_macros::Group;

/// Declares an async function as a task, and replaces it with the [`Task`] handle that
/// triggers it.
pub use ocel_macros::task;

/// Declares an async function as a consumer of a topic, handed one message at a time.
pub use ocel_macros::consumer;

/// Declares an async function as a consumer of a topic, handed its messages in batches.
pub use ocel_macros::batch_consumer;

#[doc(hidden)]
pub use declare::{
    Batch, Check, Declare, Declared, DeclaredConfig, DeclaredGroup, DeclaredResource,
    DeclaredVariable, Group, Registered, Retry,
};
#[doc(hidden)]
pub use deliver::{deliver, run_attempt, Delivery, Outcome, Steps};
#[doc(hidden)]
pub use env::{check, group_present, optional, secret, value, Boolean, Class};
#[doc(hidden)]
pub use run::BoxFuture;
#[cfg(feature = "schemars")]
#[doc(hidden)]
pub use schema::generate_json_schema;

#[doc(hidden)]
pub use inventory;

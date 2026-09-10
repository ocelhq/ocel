//! The library Rust apps import to declare the infrastructure and the environment an app
//! needs. A declaration is a struct in the app's own crate, held as a value:
//!
//! ```ignore
//! #[derive(ocel::Resources, Clone)]
//! pub struct Infra {
//!     #[ocel(name = "main", version = "17")]
//!     pub db: ocel::Postgres,
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
//! Each derive registers what the struct declares at link time, so discovery reads it by
//! running the binary with `#[ocel::main]` on its `main`, without loading a value. At
//! runtime `load` reads the bindings and the values the deploy delivered.

mod binding;
mod declare;
mod env;
mod error;
mod postgres;
#[doc(hidden)]
pub mod proto;

pub use declare::discover;
pub use env::{deployment_url, Secret};
pub use error::Error;
pub use postgres::Postgres;

/// Runs discovery before `main`, and returns from `main` once discovery is done.
pub use ocel_macros::main;

/// Declares every [`Postgres`] field of a struct, and writes the `load` that hands the
/// struct back with a handle in each field.
pub use ocel_macros::Resources;

/// Declares every field of a struct as an environment variable, and writes the `load` that
/// reads the delivered values into it.
pub use ocel_macros::Env;

#[doc(hidden)]
pub use declare::{Check, Declare, Declared, DeclaredResource, DeclaredVariable, Registered};
#[doc(hidden)]
pub use env::{check, optional, secret, value, Boolean, Class};

#[doc(hidden)]
pub use inventory;

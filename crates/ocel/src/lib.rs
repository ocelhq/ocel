//! The library Rust apps import to declare the infrastructure they need.
//!
//! A declaration is a call in a file under the project's `infra` folder:
//!
//! ```ignore
//! pub static DB: ocel::Postgres = ocel::postgres!("main");
//! ```
//!
//! The call registers the declaration at link time, so discovery reads it by running
//! the binary with `#[ocel::main]` on its `main`, and at runtime the same handle reads
//! the link the deploy delivered for that name.

mod declare;
mod error;
mod link;
mod postgres;

pub use declare::{discover, Declaration};
pub use error::Error;
pub use postgres::Postgres;

/// Runs discovery before `main`, and returns from `main` once discovery is done.
pub use ocel_macros::main;

/// The registry [`postgres!`] submits declarations to. It is what the macro expands to,
/// not a surface to call.
#[doc(hidden)]
pub use inventory;

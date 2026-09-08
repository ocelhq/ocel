use crate::declare::discovering;
use crate::link::{encoded, link};
use crate::Error;

const KIND: &str = "postgres";

/// A postgres database an app declares and reads its link from.
pub struct Postgres {
    name: &'static str,
    #[cfg(feature = "postgres")]
    pool: tokio::sync::OnceCell<sqlx::PgPool>,
}

impl Postgres {
    /// Take the handle for the database named `name`. Prefer [`postgres!`], which
    /// declares the database as well as handing back its handle.
    pub const fn new(name: &'static str) -> Self {
        Self {
            name,
            #[cfg(feature = "postgres")]
            pool: tokio::sync::OnceCell::const_new(),
        }
    }

    /// The name the database was declared under, and the name its link is delivered as.
    pub const fn name(&self) -> &'static str {
        self.name
    }

    /// The postgres URL of the delivered link, with the credentials percent-encoded. It
    /// fails when no link was delivered for the name, and during discovery.
    pub fn connection_string(&self) -> Result<String, Error> {
        let properties = self.properties("connection_string")?;
        let field = |key: &str| {
            properties
                .get(key)
                .and_then(|value| value.as_str())
                .unwrap_or_default()
                .to_string()
        };
        let port = properties
            .get("port")
            .and_then(|value| value.as_u64())
            .unwrap_or_default();
        Ok(format!(
            "postgres://{}:{}@{}:{}/{}",
            encoded(&field("username")),
            encoded(&field("password")),
            field("host"),
            port,
            encoded(&field("database")),
        ))
    }

    /// The sqlx pool over the delivered link, opened on the first call and returned as it
    /// stands on every one after. It fails when no link was delivered for the name, and
    /// during discovery.
    #[cfg(feature = "postgres")]
    pub async fn pool(&self) -> Result<&sqlx::PgPool, Error> {
        if discovering() {
            return Err(self.unprovisioned("pool"));
        }
        self.pool
            .get_or_try_init(|| async {
                Ok(sqlx::PgPool::connect(&self.connection_string()?).await?)
            })
            .await
    }

    fn properties(&self, access: &str) -> Result<serde_json::Value, Error> {
        if discovering() {
            return Err(self.unprovisioned(access));
        }
        link(self.name, KIND)
    }

    fn unprovisioned(&self, access: &str) -> Error {
        Error::Unprovisioned {
            resource: format!("postgres(\"{}\")", self.name),
            access: access.to_string(),
        }
    }
}

/// Declare a postgres database and return the handle an app reads it through. Write it in
/// a file under the project's `infra` folder:
///
/// ```ignore
/// pub static DB: ocel::Postgres = ocel::postgres!("main");
/// pub static CACHE: ocel::Postgres = ocel::postgres!("cache", version = "16");
/// ```
///
/// The declaration is registered at link time and posted by [`discover`](crate::discover),
/// so the file is never run to be read.
#[macro_export]
macro_rules! postgres {
    ($name:literal) => {{
        $crate::inventory::submit! {
            $crate::Declaration {
                kind: "postgres",
                name: $name,
                version: "17",
                file: file!(),
                line: line!(),
            }
        }
        $crate::Postgres::new($name)
    }};
    ($name:literal, version = $version:literal) => {{
        $crate::inventory::submit! {
            $crate::Declaration {
                kind: "postgres",
                name: $name,
                version: $version,
                file: file!(),
                line: line!(),
            }
        }
        $crate::Postgres::new($name)
    }};
}

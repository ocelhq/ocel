use crate::declare::discovering;
use crate::link::{encoded, postgres};
use crate::proto::common::links::v1::PostgresProperties;
use crate::Error;

pub(crate) const KIND: &str = "postgres";

/// A postgres database an app declares and reads its link from. A field of this type in a
/// struct deriving [`Resources`](macro@crate::Resources) is the declaration.
#[derive(Clone)]
pub struct Postgres {
    name: String,
    #[cfg(feature = "postgres")]
    pool: std::sync::Arc<tokio::sync::OnceCell<sqlx::PgPool>>,
}

impl Postgres {
    /// Take the handle for the database named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the database as well as
    /// handing back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            #[cfg(feature = "postgres")]
            pool: std::sync::Arc::default(),
        }
    }

    /// The name the database was declared under, and the name its link is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The postgres URL of the delivered link, with the credentials percent-encoded. It
    /// fails when no link was delivered for the name, and during discovery.
    pub fn connection_string(&self) -> Result<String, Error> {
        let properties = self.properties("connection_string")?;
        Ok(format!(
            "postgres://{}:{}@{}:{}/{}",
            encoded(&properties.username),
            encoded(&properties.password),
            properties.host,
            properties.port,
            encoded(&properties.database),
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

    fn properties(&self, access: &str) -> Result<PostgresProperties, Error> {
        if discovering() {
            return Err(self.unprovisioned(access));
        }
        postgres(&self.name)
    }

    fn unprovisioned(&self, access: &str) -> Error {
        Error::Unprovisioned {
            resource: format!("postgres(\"{}\")", self.name),
            access: access.to_string(),
        }
    }
}

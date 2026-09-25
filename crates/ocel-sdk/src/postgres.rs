use crate::binding::{encoded, postgres};
use crate::declare::discovering;
use crate::proto::common::bindings::v1::PostgresProperties;
use crate::Error;

pub(crate) const KIND: &str = "postgres";

/// A postgres database an app declares and reads its binding from. A field of this type in a
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

    /// The name the database was declared under, and the name its binding is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The postgres URL of the delivered binding: the record's url verbatim when it carries
    /// one, and otherwise one built from its host, port, database and credentials,
    /// percent-encoded, with its tls mode as `sslmode`. It fails when no binding was delivered
    /// for the name, and during discovery.
    pub fn connection_string(&self) -> Result<String, Error> {
        Ok(connection_string(&self.properties("connection_string")?))
    }

    /// The sqlx pool over the delivered binding, opened on the first call and returned as it
    /// stands on every one after. A record under verify-full that names a CA trusts that CA
    /// for the server's certificate. It fails when no binding was delivered for the name, and
    /// during discovery.
    #[cfg(feature = "postgres")]
    pub async fn pool(&self) -> Result<&sqlx::PgPool, Error> {
        if discovering() {
            return Err(self.unprovisioned("pool"));
        }
        self.pool
            .get_or_try_init(|| async {
                let options = connect_options(&self.properties("pool")?)?;
                Ok(sqlx::PgPool::connect_with(options).await?)
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

fn connection_string(properties: &PostgresProperties) -> String {
    if !properties.url.is_empty() {
        return properties.url.clone();
    }
    let sslmode = if properties.tls_mode.is_empty() {
        String::new()
    } else {
        format!("?sslmode={}", encoded(&properties.tls_mode))
    };
    format!(
        "postgres://{}:{}@{}:{}/{}{}",
        encoded(&properties.username),
        encoded(&properties.password),
        properties.host,
        properties.port,
        encoded(&properties.database),
        sslmode,
    )
}

#[cfg(feature = "postgres")]
fn connect_options(
    properties: &PostgresProperties,
) -> Result<sqlx::postgres::PgConnectOptions, Error> {
    let options: sqlx::postgres::PgConnectOptions = connection_string(properties).parse()?;
    if properties.tls_ca.is_empty() {
        return Ok(options);
    }
    Ok(options.ssl_root_cert_from_pem(properties.tls_ca.clone().into_bytes()))
}

#[cfg(all(test, feature = "postgres"))]
mod tests {
    use super::*;
    use sqlx::postgres::PgSslMode;

    fn properties(tls_mode: &str, tls_ca: &str) -> PostgresProperties {
        PostgresProperties {
            host: "db.example.com".into(),
            port: 5432,
            database: "d".into(),
            username: "u".into(),
            password: "p".into(),
            tls_mode: tls_mode.into(),
            tls_ca: tls_ca.into(),
            ..Default::default()
        }
    }

    #[test]
    fn a_url_keeps_its_own_sslmode_and_options() {
        let options = connect_options(&PostgresProperties {
            url: "postgres://app:pw@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool".into(),
            ..Default::default()
        })
        .expect("options");
        assert_eq!(options.get_host(), "ep-cool.neon.tech");
        assert!(matches!(options.get_ssl_mode(), PgSslMode::Require));
        assert!(options
            .get_options()
            .is_some_and(|o| o.contains("endpoint")));
    }

    #[test]
    fn verify_full_trusts_the_records_ca() {
        let ca = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";
        let options = connect_options(&properties("verify-full", ca)).expect("options");
        assert!(matches!(options.get_ssl_mode(), PgSslMode::VerifyFull));
        assert!(format!("{options:?}").contains("ssl_root_cert: Some(Inline("));
    }

    #[test]
    fn no_mode_leaves_the_drivers_default() {
        let options = connect_options(&properties("", "")).expect("options");
        assert!(matches!(options.get_ssl_mode(), PgSslMode::Prefer));
    }
}

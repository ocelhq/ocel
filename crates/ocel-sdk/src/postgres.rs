use crate::binding::{percent_encode, read_postgres};
use crate::declare::is_discovering;
use crate::proto::common::bindings::v1::{PostgresProperties, PostgresTlsMode};
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

    /// The postgres URL of the delivered binding: the record's url verbatim when it has
    /// one, and otherwise one built from its host, port, database and credentials,
    /// percent-encoded, with its tls mode as `sslmode`. It fails when no binding was delivered
    /// for the name, and during discovery.
    pub fn connection_string(&self) -> Result<String, Error> {
        Ok(build_connection_string(
            &self.read_properties("connection_string")?,
        ))
    }

    /// The sqlx pool over the delivered binding, opened on the first call and returned unchanged
    /// on every one after. A record under verify-full that names a CA trusts that CA
    /// for the server's certificate, and one that names a TLS server name, as a record
    /// pointing at a port forward does, verifies the certificate against that name instead of
    /// the host. It fails when no binding was delivered for the name, when such a record
    /// is read on a platform without unix sockets, and during discovery.
    #[cfg(feature = "postgres")]
    pub async fn pool(&self) -> Result<&sqlx::PgPool, Error> {
        if is_discovering() {
            return Err(self.refuse_unprovisioned("pool"));
        }
        self.pool
            .get_or_try_init(|| async {
                let options = connect_options(&self.read_properties("pool")?).await?;
                Ok(sqlx::PgPool::connect_with(options).await?)
            })
            .await
    }

    fn read_properties(&self, access: &str) -> Result<PostgresProperties, Error> {
        if is_discovering() {
            return Err(self.refuse_unprovisioned(access));
        }
        read_postgres(&self.name)
    }

    fn refuse_unprovisioned(&self, access: &str) -> Error {
        Error::Unprovisioned {
            resource: format!("postgres(\"{}\")", self.name),
            access: access.to_string(),
        }
    }
}

fn build_connection_string(properties: &PostgresProperties) -> String {
    if !properties.url.is_empty() {
        return properties.url.clone();
    }
    let sslmode = match properties.tls_mode.as_known() {
        Some(PostgresTlsMode::POSTGRES_TLS_MODE_REQUIRE) => "?sslmode=require",
        Some(PostgresTlsMode::POSTGRES_TLS_MODE_VERIFY_FULL) => "?sslmode=verify-full",
        _ => "",
    };
    format!(
        "postgres://{}:{}@{}:{}/{}{}",
        percent_encode(&properties.username),
        percent_encode(&properties.password),
        properties.host,
        properties.port,
        percent_encode(&properties.database),
        sslmode,
    )
}

#[cfg(feature = "postgres")]
fn build_connect_options(
    properties: &PostgresProperties,
) -> Result<sqlx::postgres::PgConnectOptions, Error> {
    let options: sqlx::postgres::PgConnectOptions = build_connection_string(properties).parse()?;
    if properties.tls_ca.is_empty() {
        return Ok(options);
    }
    Ok(options.ssl_root_cert_from_pem(properties.tls_ca.clone().into_bytes()))
}

#[cfg(feature = "postgres")]
async fn connect_options(
    properties: &PostgresProperties,
) -> Result<sqlx::postgres::PgConnectOptions, Error> {
    let options = build_connect_options(properties)?;
    if properties.tls_server_name.is_empty()
        || !matches!(
            options.get_ssl_mode(),
            sqlx::postgres::PgSslMode::VerifyFull
        )
    {
        return Ok(options);
    }
    relay_under_server_name(options, &properties.tls_server_name).await
}

#[cfg(all(feature = "postgres", unix))]
async fn relay_under_server_name(
    options: sqlx::postgres::PgConnectOptions,
    server_name: &str,
) -> Result<sqlx::postgres::PgConnectOptions, Error> {
    let target = format!("{}:{}", options.get_host(), options.get_port());
    let relay = RelayDir::create().map_err(sqlx::Error::Io)?;
    let listener =
        tokio::net::UnixListener::bind(relay.path.join(format!(".s.PGSQL.{}", options.get_port())))
            .map_err(sqlx::Error::Io)?;
    let socket = relay.path.clone();
    tokio::spawn(async move {
        let _relay = relay;
        while let Ok((mut accepted, _)) = listener.accept().await {
            let target = target.clone();
            tokio::spawn(async move {
                if let Ok(mut forwarded) = tokio::net::TcpStream::connect(target).await {
                    let _ = tokio::io::copy_bidirectional(&mut accepted, &mut forwarded).await;
                }
            });
        }
    });
    Ok(options.host(server_name).socket(socket))
}

#[cfg(all(feature = "postgres", not(unix)))]
async fn relay_under_server_name(
    _options: sqlx::postgres::PgConnectOptions,
    server_name: &str,
) -> Result<sqlx::postgres::PgConnectOptions, Error> {
    Err(sqlx::Error::Configuration(
        format!("this binding verifies its server as {server_name} through a port forward, and sqlx verifies only the host it connects to, which on this platform is the forward").into(),
    )
    .into())
}

#[cfg(all(feature = "postgres", unix))]
struct RelayDir {
    path: std::path::PathBuf,
}

#[cfg(all(feature = "postgres", unix))]
impl RelayDir {
    fn create() -> std::io::Result<Self> {
        use std::os::unix::fs::DirBuilderExt;
        static CREATED: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let path = std::env::temp_dir().join(format!(
            "ocel-pg-{}-{}",
            std::process::id(),
            CREATED.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        std::fs::DirBuilder::new().mode(0o700).create(&path)?;
        Ok(Self { path })
    }
}

#[cfg(all(feature = "postgres", unix))]
impl Drop for RelayDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.path);
    }
}

#[cfg(all(test, feature = "postgres"))]
mod tests {
    use super::*;
    use sqlx::postgres::PgSslMode;

    fn new_properties(tls_mode: PostgresTlsMode, tls_ca: &str) -> PostgresProperties {
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
        let options = build_connect_options(&PostgresProperties {
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
        let options = build_connect_options(&new_properties(
            PostgresTlsMode::POSTGRES_TLS_MODE_VERIFY_FULL,
            ca,
        ))
        .expect("options");
        assert!(matches!(options.get_ssl_mode(), PgSslMode::VerifyFull));
        assert!(format!("{options:?}").contains("ssl_root_cert: Some(Inline("));
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn verify_full_through_a_forward_verifies_the_tls_server_name_and_reaches_the_forward() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};

        let forward = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = forward.local_addr().unwrap().port();
        let options = connect_options(&PostgresProperties {
            host: "127.0.0.1".into(),
            port: port.into(),
            database: "d".into(),
            username: "u".into(),
            password: "p".into(),
            tls_mode: PostgresTlsMode::POSTGRES_TLS_MODE_VERIFY_FULL.into(),
            tls_server_name: "orders.cluster.internal".into(),
            ..Default::default()
        })
        .await
        .expect("options");

        assert_eq!(options.get_host(), "orders.cluster.internal");
        let socket = options
            .get_socket()
            .expect("the relay the driver connects through");
        let mut relayed = tokio::net::UnixStream::connect(socket.join(format!(".s.PGSQL.{port}")))
            .await
            .unwrap();
        relayed.write_all(b"startup").await.unwrap();
        let (mut accepted, _) = forward.accept().await.unwrap();
        let mut read = [0u8; 7];
        accepted.read_exact(&mut read).await.unwrap();
        assert_eq!(&read, b"startup");
    }

    #[tokio::test]
    async fn a_forward_under_require_is_reached_directly() {
        let options = connect_options(&PostgresProperties {
            tls_server_name: "orders.cluster.internal".into(),
            ..new_properties(PostgresTlsMode::POSTGRES_TLS_MODE_REQUIRE, "")
        })
        .await
        .expect("options");

        assert_eq!(options.get_host(), "db.example.com");
        assert!(options.get_socket().is_none());
    }

    #[test]
    fn no_mode_leaves_the_drivers_default() {
        let options = build_connect_options(&new_properties(
            PostgresTlsMode::POSTGRES_TLS_MODE_UNSPECIFIED,
            "",
        ))
        .expect("options");
        assert!(matches!(options.get_ssl_mode(), PgSslMode::Prefer));
    }
}

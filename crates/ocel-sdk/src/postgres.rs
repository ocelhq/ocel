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
                let (options, relay) = connect_options(&self.read_properties("pool")?)?;
                Ok::<_, Error>(pool_options(relay).connect_with(options).await?)
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
fn pool_options(relay: Option<Relay>) -> sqlx::postgres::PgPoolOptions {
    sqlx::postgres::PgPoolOptions::new().after_connect(move |_, _| {
        let _ = &relay;
        Box::pin(async { Ok(()) })
    })
}

#[cfg(feature = "postgres")]
fn connect_options(
    properties: &PostgresProperties,
) -> Result<(sqlx::postgres::PgConnectOptions, Option<Relay>), Error> {
    let options = build_connect_options(properties)?;
    if properties.tls_server_name.is_empty()
        || !matches!(
            options.get_ssl_mode(),
            sqlx::postgres::PgSslMode::VerifyFull
        )
    {
        return Ok((options, None));
    }
    let relay = Relay::start(
        format!("{}:{}", options.get_host(), options.get_port()),
        options.get_port(),
        &properties.tls_server_name,
    )?;
    let options = options
        .host(&properties.tls_server_name)
        .socket(relay.dir.path.clone());
    Ok((options, Some(relay)))
}

#[cfg(all(feature = "postgres", unix))]
struct Relay {
    dir: RelayDir,
    socket: std::path::PathBuf,
    stopped: std::sync::Arc<std::sync::atomic::AtomicBool>,
}

#[cfg(all(feature = "postgres", unix))]
impl Relay {
    fn start(target: String, port: u16, _server_name: &str) -> Result<Self, Error> {
        use std::sync::atomic::{AtomicBool, Ordering};
        let dir = RelayDir::create().map_err(sqlx::Error::Io)?;
        let socket = dir.path.join(format!(".s.PGSQL.{port}"));
        let listener = std::os::unix::net::UnixListener::bind(&socket).map_err(sqlx::Error::Io)?;
        let stopped = std::sync::Arc::new(AtomicBool::new(false));
        let stopping = stopped.clone();
        std::thread::Builder::new()
            .name("ocel-postgres-relay".into())
            .spawn(move || {
                for accepted in listener.incoming() {
                    if stopping.load(Ordering::SeqCst) {
                        return;
                    }
                    let Ok(accepted) = accepted else { return };
                    let target = target.clone();
                    std::thread::spawn(move || carry(accepted, &target));
                }
            })
            .map_err(sqlx::Error::Io)?;
        Ok(Self {
            dir,
            socket,
            stopped,
        })
    }
}

#[cfg(all(feature = "postgres", unix))]
impl Drop for Relay {
    fn drop(&mut self) {
        self.stopped
            .store(true, std::sync::atomic::Ordering::SeqCst);
        let _ = std::os::unix::net::UnixStream::connect(&self.socket);
    }
}

#[cfg(all(feature = "postgres", unix))]
fn carry(accepted: std::os::unix::net::UnixStream, target: &str) {
    use std::net::Shutdown;
    let Ok(forwarded) = std::net::TcpStream::connect(target) else {
        return;
    };
    let (Ok(mut from_driver), Ok(mut to_forward)) = (accepted.try_clone(), forwarded.try_clone())
    else {
        return;
    };
    let upstream = std::thread::spawn(move || {
        let _ = std::io::copy(&mut from_driver, &mut to_forward);
        let _ = to_forward.shutdown(Shutdown::Write);
    });
    let (mut from_forward, mut to_driver) = (forwarded, accepted);
    let _ = std::io::copy(&mut from_forward, &mut to_driver);
    let _ = to_driver.shutdown(Shutdown::Write);
    let _ = upstream.join();
}

#[cfg(all(feature = "postgres", not(unix)))]
struct Relay {
    dir: RelayDir,
}

#[cfg(all(feature = "postgres", not(unix)))]
struct RelayDir {
    path: std::path::PathBuf,
}

#[cfg(all(feature = "postgres", not(unix)))]
impl Relay {
    fn start(_target: String, _port: u16, server_name: &str) -> Result<Self, Error> {
        Err(sqlx::Error::Configuration(
            format!("this binding verifies its server as {server_name} through a port forward, and sqlx verifies only the host it connects to, which on this platform is the forward").into(),
        )
        .into())
    }
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
    fn forwarded_properties(port: u16) -> PostgresProperties {
        PostgresProperties {
            host: "127.0.0.1".into(),
            port: port.into(),
            database: "d".into(),
            username: "u".into(),
            password: "p".into(),
            tls_mode: PostgresTlsMode::POSTGRES_TLS_MODE_VERIFY_FULL.into(),
            tls_server_name: "orders.cluster.internal".into(),
            ..Default::default()
        }
    }

    #[cfg(unix)]
    #[test]
    fn a_relay_stops_and_removes_its_socket_once_dropped() {
        let forward = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = forward.local_addr().unwrap().port();
        let (options, relay) = connect_options(&forwarded_properties(port)).expect("options");
        let socket = options
            .get_socket()
            .expect("the relay the driver connects through")
            .clone();

        drop(relay);

        assert!(
            !socket.exists(),
            "the relay's directory {socket:?} outlived it"
        );
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn a_relay_lives_until_the_last_clone_of_its_pool_is_dropped() {
        let forward = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = forward.local_addr().unwrap().port();
        let (options, relay) = connect_options(&forwarded_properties(port)).expect("options");
        let socket = options
            .get_socket()
            .expect("the relay the driver connects through")
            .clone();
        let pool = pool_options(relay).connect_lazy_with(options);
        let cloned = pool.clone();

        drop(pool);
        assert!(
            socket.exists(),
            "the relay's directory {socket:?} was removed while a clone of its pool lived"
        );

        drop(cloned);
        assert!(
            !socket.exists(),
            "the relay's directory {socket:?} outlived every clone of its pool"
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_relay_outlives_the_runtime_that_opened_it() {
        use std::io::{Read, Write};

        let forward = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = forward.local_addr().unwrap().port();
        let opened = tokio::runtime::Builder::new_current_thread()
            .build()
            .unwrap()
            .block_on(async { connect_options(&forwarded_properties(port)) });
        let (options, _relay) = opened.expect("options");

        let mut relayed = std::os::unix::net::UnixStream::connect(
            options
                .get_socket()
                .unwrap()
                .join(format!(".s.PGSQL.{port}")),
        )
        .unwrap();
        relayed.write_all(b"startup").unwrap();
        let (mut accepted, _) = forward.accept().unwrap();
        let mut read = [0u8; 7];
        accepted.read_exact(&mut read).unwrap();
        assert_eq!(&read, b"startup");
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn verify_full_through_a_forward_verifies_the_tls_server_name_and_reaches_the_forward() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};

        let forward = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = forward.local_addr().unwrap().port();
        let (options, _relay) = connect_options(&PostgresProperties {
            host: "127.0.0.1".into(),
            port: port.into(),
            database: "d".into(),
            username: "u".into(),
            password: "p".into(),
            tls_mode: PostgresTlsMode::POSTGRES_TLS_MODE_VERIFY_FULL.into(),
            tls_server_name: "orders.cluster.internal".into(),
            ..Default::default()
        })
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

    #[test]
    fn a_forward_under_require_is_reached_directly() {
        let (options, _relay) = connect_options(&PostgresProperties {
            tls_server_name: "orders.cluster.internal".into(),
            ..new_properties(PostgresTlsMode::POSTGRES_TLS_MODE_REQUIRE, "")
        })
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

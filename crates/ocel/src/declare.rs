use crate::postgres::KIND;
use crate::r#gen::app::resources::v1::declare_request::Config;
use crate::r#gen::app::resources::v1::{
    DeclareRequest, PostgresConfig, ResourceIdentifier, ResourceServiceClient,
};
use crate::r#gen::common::links::v1::LinkType;
use crate::Error;

const PHASE_ENV: &str = "OCEL_PHASE";
const DEV_SERVER_ENV: &str = "OCEL_DEV_SERVER";
const SOURCE_ROOT_ENV: &str = "OCEL_SOURCE_ROOT";
const DISCOVERY_PHASE: &str = "discovery";

/// One postgres database a file declared, as the declaring macro registered it.
pub struct Declaration {
    /// The name the database is declared under.
    pub name: &'static str,
    /// The version to provision.
    pub version: &'static str,
    /// The path of the file the declaration is written in, as the compiler saw it.
    pub file: &'static str,
    /// The line the declaration is written on.
    pub line: u32,
}

inventory::collect!(Declaration);

/// Post every declaration this binary links to the dev server and report whether the
/// run was discovery. A `false` means the app should carry on and serve.
///
/// [`macro@main`](crate::main) calls this, so an app that carries the attribute never
/// calls it itself. The client the declarations go over is async, and the runtime it
/// runs on is this call's own thread, so `discover` blocks whether or not the app has
/// a runtime of its own.
pub fn discover() -> Result<bool, Error> {
    if !discovering() {
        return Ok(false);
    }
    std::thread::scope(|scope| {
        scope
            .spawn(|| {
                tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                    .expect("a runtime to post declarations on")
                    .block_on(post_all())
            })
            .join()
            .expect("the thread that posts declarations")
    })?;
    Ok(true)
}

pub(crate) fn discovering() -> bool {
    std::env::var(PHASE_ENV).as_deref() == Ok(DISCOVERY_PHASE)
}

async fn post_all() -> Result<(), Error> {
    if inventory::iter::<Declaration>.into_iter().next().is_none() {
        return Ok(());
    }
    let server = std::env::var(DEV_SERVER_ENV).unwrap_or_default();
    let Ok(base) = server.trim_end_matches('/').parse() else {
        return Err(Error::DevServer { server });
    };
    let client = ResourceServiceClient::new(
        connectrpc::client::HttpClient::plaintext(),
        connectrpc::client::ClientConfig::new(base),
    );
    for declaration in inventory::iter::<Declaration> {
        client
            .declare(request(declaration))
            .await
            .map_err(|err| failed(declaration, err.to_string()))?;
    }
    Ok(())
}

fn request(declaration: &Declaration) -> DeclareRequest {
    DeclareRequest {
        resource: ResourceIdentifier {
            r#type: LinkType::LINK_TYPE_POSTGRES.into(),
            name: declaration.name.to_string(),
            ..Default::default()
        }
        .into(),
        config: Some(Config::from(PostgresConfig {
            version: declaration.version.to_string(),
            ..Default::default()
        })),
        source: source(declaration),
        ..Default::default()
    }
}

fn source(declaration: &Declaration) -> String {
    let file = std::path::Path::new(declaration.file);
    let absolute = if file.is_absolute() {
        file.to_path_buf()
    } else {
        source_root().join(file)
    };
    format!("{}:{}", absolute.display(), declaration.line)
}

fn source_root() -> std::path::PathBuf {
    match std::env::var(SOURCE_ROOT_ENV) {
        Ok(root) if !root.is_empty() => std::path::PathBuf::from(root),
        _ => std::env::current_dir().unwrap_or_default(),
    }
}

fn failed(declaration: &Declaration, said: String) -> Error {
    Error::Declare {
        kind: KIND.to_string(),
        name: declaration.name.to_string(),
        said,
    }
}

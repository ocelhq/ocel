use crate::Error;

const PHASE_ENV: &str = "OCEL_PHASE";
const DEV_SERVER_ENV: &str = "OCEL_DEV_SERVER";
const DISCOVERY_PHASE: &str = "discovery";
const DECLARE_PATH: &str = "/app.resources.v1.ResourceService/Declare";

/// One resource a file declared, as the declaring macro registered it.
pub struct Declaration {
    /// The kind of resource, lowercase, as the wire names its config.
    pub kind: &'static str,
    /// The name the resource is declared under.
    pub name: &'static str,
    /// The version to provision.
    pub version: &'static str,
    /// The absolute path of the file the declaration is written in.
    pub file: &'static str,
    /// The line the declaration is written on.
    pub line: u32,
}

inventory::collect!(Declaration);

/// Post every declaration this binary links to the dev server and report whether the
/// run was discovery. A `false` means the app should carry on and serve.
///
/// [`macro@main`] calls this, so an app that carries the attribute never calls it itself.
pub fn discover() -> Result<bool, Error> {
    if std::env::var(PHASE_ENV).as_deref() != Ok(DISCOVERY_PHASE) {
        return Ok(false);
    }
    for declaration in inventory::iter::<Declaration> {
        post(declaration)?;
    }
    Ok(true)
}

pub(crate) fn discovering() -> bool {
    std::env::var(PHASE_ENV).as_deref() == Ok(DISCOVERY_PHASE)
}

fn post(declaration: &Declaration) -> Result<(), Error> {
    let server = std::env::var(DEV_SERVER_ENV).unwrap_or_default();
    let url = format!("{}{}", server.trim_end_matches('/'), DECLARE_PATH);

    let mut body = serde_json::Map::new();
    body.insert(
        "resource".to_string(),
        serde_json::json!({
            "type": format!("LINK_TYPE_{}", declaration.kind.to_uppercase()),
            "name": declaration.name,
        }),
    );
    body.insert(
        declaration.kind.to_string(),
        serde_json::json!({"version": declaration.version}),
    );
    body.insert(
        "source".to_string(),
        serde_json::json!(format!("{}:{}", declaration.file, declaration.line)),
    );

    let agent: ureq::Agent = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .build()
        .into();
    let mut response = agent
        .post(&url)
        .send_json(serde_json::Value::Object(body))
        .map_err(|err| failed(declaration, err.to_string()))?;

    if response.status() != 200 {
        let status = response.status();
        let said = response.body_mut().read_to_string().unwrap_or_default();
        return Err(failed(
            declaration,
            format!("{} {}", status.as_u16(), said.trim()),
        ));
    }
    Ok(())
}

fn failed(declaration: &Declaration, said: String) -> Error {
    Error::Declare {
        kind: declaration.kind.to_string(),
        name: declaration.name.to_string(),
        said,
    }
}

use crate::env::{complaint, Class};
use crate::proto::app::resources::v1::variable_problem::Kind;
use crate::proto::app::resources::v1::{
    DeclareEnvRequest, DeclareRequest, GroupDefinition, ReportEnvProblemsRequest,
    ResourceServiceClient, VariableCell, VariableClass, VariableDefinition, VariableProblem,
};
#[cfg(feature = "realtime")]
use crate::proto::app::resources::v1::{
    RealtimeChannel, RealtimeConfig, RealtimePublish, RealtimeSubscribe, ResourceIdentifier,
    ResourceType,
};
#[cfg(feature = "realtime")]
use crate::realtime::{list_declared_channels, resolve_token_ttl, DeclaredChannel};
use crate::Error;
use buffa::Enumeration;
use std::time::Duration;

const PHASE_ENV: &str = "OCEL_PHASE";
const DEV_SERVER_ENV: &str = "OCEL_DEV_SERVER";
const DEV_SERVER_TOKEN_ENV: &str = "OCEL_DEV_SERVER_TOKEN";
const SOURCE_ROOT_ENV: &str = "OCEL_SOURCE_ROOT";
const DISCOVERY_PHASE: &str = "discovery";
const SDK_VERSION_HEADER: &str = "ocel-sdk-version";
const SDK_VERSION: &str = concat!("rust/", env!("CARGO_PKG_VERSION"));

#[doc(hidden)]
pub fn proto_duration(
    duration: Option<Duration>,
) -> Option<buffa_types::google::protobuf::Duration> {
    duration.map(Into::into)
}

#[doc(hidden)]
pub struct DeclaredResource {
    pub request: DeclareRequest,
    pub file: &'static str,
    pub line: u32,
}

#[doc(hidden)]
pub type Check = fn(&str) -> Result<(), String>;

#[doc(hidden)]
pub struct DeclaredVariable {
    pub key: &'static str,
    pub class: Class,
    pub required: bool,
    pub folders: &'static [&'static str],
    pub description: Option<&'static str>,
    pub file: &'static str,
    pub line: u32,
    pub check: Option<Check>,
    pub group: Option<&'static str>,
}

#[doc(hidden)]
pub struct DeclaredGroup {
    pub key: &'static str,
    pub required: bool,
    pub description: Option<&'static str>,
    pub members: fn() -> Declared,
    pub file: &'static str,
    pub line: u32,
}

#[doc(hidden)]
pub struct Declared {
    pub resources: Vec<DeclaredResource>,
    pub variables: Vec<DeclaredVariable>,
    pub groups: Vec<DeclaredGroup>,
}

#[doc(hidden)]
pub trait Declare: Sized {
    fn declared() -> Declared;
    fn load() -> Result<Self, Error>;
}

#[doc(hidden)]
#[diagnostic::on_unimplemented(
    message = "`{Self}` is not an ocel::Group, so no #[ocel(group)] field can have it as its type.",
    label = "this type must be an ocel::Group",
    note = "a group's struct uses #[derive(ocel::Group)], not #[derive(ocel::Env)], and contains no #[ocel(group)] fields of its own: a group nests one level only."
)]
pub trait Group: Declare {}

#[doc(hidden)]
pub struct Registered(pub fn() -> Declared);

inventory::collect!(Registered);

/// Post everything the structs this binary links declare to the dev server, and report
/// whether the run was discovery. A `false` means the app should continue and serve.
///
/// [`macro@main`](crate::main) calls this, so an app whose `main` has the attribute never
/// calls it itself. The client the declarations go over is async, and the runtime it
/// runs on is this call's own thread, so `discover` blocks whether or not the app has
/// a runtime of its own.
pub fn discover() -> Result<bool, Error> {
    if !is_discovering() {
        return Ok(false);
    }
    let declared = collect_declarations()?;
    std::thread::scope(|scope| {
        scope
            .spawn(|| {
                tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                    .expect("a runtime to post declarations on")
                    .block_on(post_all(&declared))
            })
            .join()
            .expect("the thread that posts declarations")
    })?;
    Ok(true)
}

pub(crate) fn is_discovering() -> bool {
    std::env::var(PHASE_ENV).as_deref() == Ok(DISCOVERY_PHASE)
}

fn collect_declarations() -> Result<Declared, Error> {
    let mut structs: Vec<Declared> = inventory::iter::<Registered>
        .into_iter()
        .map(|registered| (registered.0)())
        .collect();
    structs.sort_by_key(find_first_site);

    let mut all = Declared {
        resources: Vec::new(),
        variables: Vec::new(),
        groups: Vec::new(),
    };
    let mut variable_owners: Vec<(&str, String)> = Vec::new();
    let mut group_owners: Vec<(&str, String)> = Vec::new();

    for one in structs {
        all.resources.extend(one.resources);
        for variable in one.variables {
            claim(
                &mut variable_owners,
                variable.key,
                format_site(variable.file, variable.line),
                "A key is declared exactly once, in exactly one file.",
            )?;
            all.variables.push(variable);
        }
        for group in one.groups {
            claim(
                &mut group_owners,
                group.key,
                format_site(group.file, group.line),
                "A group is declared exactly once, in exactly one file.",
            )?;
            all.groups.push(group);
        }
    }
    #[cfg(feature = "realtime")]
    all.resources.extend(collect_realtime_resources()?);
    join_group_members(&mut all)?;
    Ok(all)
}

#[cfg(feature = "realtime")]
fn collect_realtime_resources() -> Result<Vec<DeclaredResource>, Error> {
    let mut resources: Vec<Vec<DeclaredChannel>> = Vec::new();
    for channel in list_declared_channels() {
        match resources
            .iter_mut()
            .find(|channels| channels[0].realtime == channel.realtime)
        {
            Some(channels) => channels.push(channel),
            None => resources.push(vec![channel]),
        }
    }
    resources
        .iter()
        .map(|channels| {
            let first = &channels[0];
            let token_ttl = resolve_token_ttl(channels).map_err(|detail| Error::Definition {
                key: first.realtime.to_string(),
                detail,
            })?;
            Ok(DeclaredResource {
                request: DeclareRequest {
                    resource: ResourceIdentifier {
                        r#type: ResourceType::RESOURCE_TYPE_REALTIME.into(),
                        name: first.realtime.to_string(),
                        ..Default::default()
                    }
                    .into(),
                    config: Some(
                        RealtimeConfig {
                            channels: channels.iter().map(build_channel).collect(),
                            token_ttl: buffa::MessageField::some(token_ttl.into()),
                            ..Default::default()
                        }
                        .into(),
                    ),
                    ..Default::default()
                },
                file: first.file,
                line: first.line,
            })
        })
        .collect()
}

#[cfg(feature = "realtime")]
fn build_channel(channel: &DeclaredChannel) -> RealtimeChannel {
    RealtimeChannel {
        pattern: channel.pattern.to_string(),
        wildcard: channel.wildcard,
        schema: channel.schema.map(|schema| schema()).unwrap_or_default(),
        subscribe: if channel.public {
            RealtimeSubscribe::REALTIME_SUBSCRIBE_PUBLIC
        } else {
            RealtimeSubscribe::REALTIME_SUBSCRIBE_RULE
        }
        .into(),
        publish: if channel.publish {
            RealtimePublish::REALTIME_PUBLISH_RULE
        } else {
            RealtimePublish::REALTIME_PUBLISH_SERVER
        }
        .into(),
        source: format_source(channel.file, channel.line),
        ..Default::default()
    }
}

fn join_group_members(all: &mut Declared) -> Result<(), Error> {
    for index in 0..all.groups.len() {
        let (key, members) = (all.groups[index].key, (all.groups[index].members)());
        for mut member in members.variables {
            if let Some(existing) = all
                .variables
                .iter()
                .find(|existing| existing.key == member.key)
            {
                let detail = match existing.group {
                    Some(seen) => format!(
                        "belongs to the group '{seen}' and to the group '{key}'. A variable belongs to one group."
                    ),
                    None => {
                        let (existing, member) = (
                            format_site(existing.file, existing.line),
                            format_site(member.file, member.line),
                        );
                        format!(
                            "is declared in {existing} and in the group '{key}' at {member}. A key is declared exactly once, in exactly one file."
                        )
                    }
                };
                return Err(Error::Definition {
                    key: member.key.to_string(),
                    detail,
                });
            }
            member.group = Some(key);
            all.variables.push(member);
        }
    }
    Ok(())
}

fn find_first_site(one: &Declared) -> (&'static str, u32) {
    let resources = one
        .resources
        .iter()
        .map(|resource| (resource.file, resource.line));
    let variables = one
        .variables
        .iter()
        .map(|variable| (variable.file, variable.line));
    let groups = one.groups.iter().map(|group| (group.file, group.line));
    resources
        .chain(variables)
        .chain(groups)
        .min()
        .unwrap_or_default()
}

fn format_site(file: &str, line: u32) -> String {
    format!("{file}:{line}")
}

fn claim<'a>(
    owners: &mut Vec<(&'a str, String)>,
    name: &'a str,
    site: String,
    rule: &str,
) -> Result<(), Error> {
    if let Some((_, claimed)) = owners.iter().find(|(seen, _)| *seen == name) {
        return Err(Error::Definition {
            key: name.to_string(),
            detail: format!("is declared in {claimed} and {site}. {rule}"),
        });
    }
    owners.push((name, site));
    Ok(())
}

async fn post_all(declared: &Declared) -> Result<(), Error> {
    if declared.resources.is_empty() && declared.variables.is_empty() {
        return Ok(());
    }
    let server = std::env::var(DEV_SERVER_ENV).unwrap_or_default();
    let Ok(base) = server.trim_end_matches('/').parse() else {
        return Err(Error::DevServer { server });
    };
    let token = std::env::var(DEV_SERVER_TOKEN_ENV).unwrap_or_default();
    let client = ResourceServiceClient::new(
        connectrpc::client::HttpClient::plaintext(),
        connectrpc::client::ClientConfig::new(base)
            .with_default_header("authorization", format!("Bearer {token}"))
            .with_default_header(SDK_VERSION_HEADER, SDK_VERSION),
    );
    for resource in &declared.resources {
        client
            .declare(build_declare_request(resource))
            .await
            .map_err(|err| refuse_declaration(resource, err.to_string()))?;
    }
    if declared.variables.is_empty() {
        return Ok(());
    }

    let response = client
        .declare_env(DeclareEnvRequest {
            definitions: declared.variables.iter().map(build_definition).collect(),
            groups: declared.groups.iter().map(build_group_definition).collect(),
            ..Default::default()
        })
        .await
        .map_err(|err| Error::DeclareEnv {
            said: err.to_string(),
        })?;

    let problems = find_problems(declared, &response.into_owned().cells);
    if problems.is_empty() {
        return Ok(());
    }
    client
        .report_env_problems(ReportEnvProblemsRequest {
            problems,
            ..Default::default()
        })
        .await
        .map_err(|err| Error::DeclareEnv {
            said: err.to_string(),
        })?;
    Ok(())
}

fn build_definition(variable: &DeclaredVariable) -> VariableDefinition {
    VariableDefinition {
        key: variable.key.to_string(),
        class: convert_class(variable.class).into(),
        required: variable.required,
        folders: variable.folders.iter().map(|f| f.to_string()).collect(),
        source: format_source(variable.file, variable.line),
        description: variable.description.unwrap_or_default().to_string(),
        group: variable.group.unwrap_or_default().to_string(),
        ..Default::default()
    }
}

fn build_group_definition(group: &DeclaredGroup) -> GroupDefinition {
    GroupDefinition {
        key: group.key.to_string(),
        required: group.required,
        description: group.description.unwrap_or_default().to_string(),
        ..Default::default()
    }
}

fn convert_class(class: Class) -> VariableClass {
    match class {
        Class::Plain => VariableClass::VARIABLE_CLASS_PLAIN,
        Class::Sensitive => VariableClass::VARIABLE_CLASS_SENSITIVE,
        Class::Secret => VariableClass::VARIABLE_CLASS_SECRET,
    }
}

fn find_problems(declared: &Declared, cells: &[VariableCell]) -> Vec<VariableProblem> {
    let mut problems = Vec::new();
    for variable in &declared.variables {
        let Some(check) = variable.check else {
            continue;
        };
        for cell in cells.iter().filter(|cell| cell.key == variable.key) {
            if let Err(said) = check(&cell.value) {
                problems.push(new_problem(
                    variable.key,
                    &cell.folder,
                    Kind::KIND_INVALID,
                    complaint(variable.class, &said),
                ));
            }
        }
    }
    problems
}

fn new_problem(key: &str, folder: &str, kind: Kind, detail: String) -> VariableProblem {
    VariableProblem {
        key: key.to_string(),
        folder: folder.to_string(),
        kind: kind.into(),
        detail,
        ..Default::default()
    }
}

fn build_declare_request(resource: &DeclaredResource) -> DeclareRequest {
    DeclareRequest {
        source: format_source(resource.file, resource.line),
        ..resource.request.clone()
    }
}

pub(crate) fn format_source(file: &str, line: u32) -> String {
    let path = std::path::Path::new(file);
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        read_source_root().join(path)
    };
    format!("{}:{}", absolute.display(), line)
}

fn read_source_root() -> std::path::PathBuf {
    match std::env::var(SOURCE_ROOT_ENV) {
        Ok(root) if !root.is_empty() => std::path::PathBuf::from(root),
        _ => std::env::current_dir().unwrap_or_default(),
    }
}

fn refuse_declaration(resource: &DeclaredResource, said: String) -> Error {
    let identifier = &resource.request.resource;
    let kind = identifier
        .r#type
        .as_known()
        .map(|known| known.proto_name())
        .unwrap_or_default();
    Error::Declare {
        kind: kind
            .trim_start_matches("RESOURCE_TYPE_")
            .to_ascii_lowercase(),
        name: identifier.name.clone(),
        said,
    }
}

use crate::env::{complaint, Class};
use crate::postgres::KIND;
use crate::proto::app::resources::v1::declare_request::Config;
use crate::proto::app::resources::v1::variable_problem::Kind;
use crate::proto::app::resources::v1::ResourceType;
use crate::proto::app::resources::v1::{
    DeclareEnvRequest, DeclareRequest, GroupDefinition, PostgresConfig, ReportEnvProblemsRequest,
    ResourceIdentifier, ResourceServiceClient, VariableCell, VariableClass, VariableDefinition,
    VariableProblem,
};
use crate::Error;

const PHASE_ENV: &str = "OCEL_PHASE";
const DEV_SERVER_ENV: &str = "OCEL_DEV_SERVER";
const SOURCE_ROOT_ENV: &str = "OCEL_SOURCE_ROOT";
const DISCOVERY_PHASE: &str = "discovery";

#[doc(hidden)]
pub struct DeclaredResource {
    pub name: &'static str,
    pub version: &'static str,
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
    message = "`{Self}` holds #[ocel(group)] fields of its own, and a group nests one level only.",
    label = "this cannot be a group, because it is already made of groups",
    note = "flatten the inner group's fields into `{Self}`, or declare it beside this group rather than inside it."
)]
pub trait Group: Declare {}

#[doc(hidden)]
pub struct Registered(pub fn() -> Declared);

inventory::collect!(Registered);

/// Post everything the structs this binary links declare to the dev server, and report
/// whether the run was discovery. A `false` means the app should carry on and serve.
///
/// [`macro@main`](crate::main) calls this, so an app that carries the attribute never
/// calls it itself. The client the declarations go over is async, and the runtime it
/// runs on is this call's own thread, so `discover` blocks whether or not the app has
/// a runtime of its own.
pub fn discover() -> Result<bool, Error> {
    if !discovering() {
        return Ok(false);
    }
    let declared = collected()?;
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

pub(crate) fn discovering() -> bool {
    std::env::var(PHASE_ENV).as_deref() == Ok(DISCOVERY_PHASE)
}

fn collected() -> Result<Declared, Error> {
    let mut structs: Vec<Declared> = inventory::iter::<Registered>
        .into_iter()
        .map(|registered| (registered.0)())
        .collect();
    structs.sort_by_key(order);

    let mut all = Declared {
        resources: Vec::new(),
        variables: Vec::new(),
        groups: Vec::new(),
    };
    let mut resource_owners: Vec<(&str, String)> = Vec::new();
    let mut variable_owners: Vec<(&str, String)> = Vec::new();
    let mut group_owners: Vec<(&str, String)> = Vec::new();

    for one in structs {
        for resource in one.resources {
            claim(
                &mut resource_owners,
                resource.name,
                site(resource.file, resource.line),
                "A resource name is declared exactly once, in exactly one file.",
            )?;
            all.resources.push(resource);
        }
        for variable in one.variables {
            claim(
                &mut variable_owners,
                variable.key,
                site(variable.file, variable.line),
                "A key is declared exactly once, in exactly one file.",
            )?;
            all.variables.push(variable);
        }
        for group in one.groups {
            claim(
                &mut group_owners,
                group.key,
                site(group.file, group.line),
                "A group is declared exactly once, in exactly one file.",
            )?;
            all.groups.push(group);
        }
    }
    joined(&mut all)?;
    Ok(all)
}

fn joined(all: &mut Declared) -> Result<(), Error> {
    for index in 0..all.groups.len() {
        let (key, members) = (all.groups[index].key, (all.groups[index].members)());
        for member in members.variables {
            let Some(held) = all.variables.iter_mut().find(|held| held.key == member.key) else {
                return Err(Error::Definition {
                    key: member.key.to_string(),
                    detail: format!("belongs to the group '{key}', and nothing declares it."),
                });
            };
            if let Some(seen) = held.group {
                return Err(Error::Definition {
                    key: member.key.to_string(),
                    detail: format!(
                        "belongs to the group '{seen}' and to the group '{key}'. A variable belongs to one group."
                    ),
                });
            }
            held.group = Some(key);
        }
    }
    Ok(())
}

fn order(one: &Declared) -> (&'static str, u32) {
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

fn site(file: &str, line: u32) -> String {
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
    let client = ResourceServiceClient::new(
        connectrpc::client::HttpClient::plaintext(),
        connectrpc::client::ClientConfig::new(base),
    );
    for resource in &declared.resources {
        client
            .declare(request(resource))
            .await
            .map_err(|err| failed(resource, err.to_string()))?;
    }
    if declared.variables.is_empty() {
        return Ok(());
    }

    let response = client
        .declare_env(DeclareEnvRequest {
            definitions: declared.variables.iter().map(definition).collect(),
            groups: declared.groups.iter().map(group).collect(),
            ..Default::default()
        })
        .await
        .map_err(|err| Error::DeclareEnv {
            said: err.to_string(),
        })?;

    let problems = validate(declared, &response.into_owned().cells);
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

fn definition(variable: &DeclaredVariable) -> VariableDefinition {
    VariableDefinition {
        key: variable.key.to_string(),
        class: class(variable.class).into(),
        required: variable.required,
        folders: variable.folders.iter().map(|f| f.to_string()).collect(),
        source: source(variable.file, variable.line),
        has_schema: variable.check.is_some(),
        description: variable.description.unwrap_or_default().to_string(),
        group: variable.group.unwrap_or_default().to_string(),
        ..Default::default()
    }
}

fn group(group: &DeclaredGroup) -> GroupDefinition {
    GroupDefinition {
        key: group.key.to_string(),
        required: group.required,
        description: group.description.unwrap_or_default().to_string(),
        ..Default::default()
    }
}

fn class(class: Class) -> VariableClass {
    match class {
        Class::Plain => VariableClass::VARIABLE_CLASS_PLAIN,
        Class::Sensitive => VariableClass::VARIABLE_CLASS_SENSITIVE,
        Class::Secret => VariableClass::VARIABLE_CLASS_SECRET,
    }
}

fn validate(declared: &Declared, cells: &[VariableCell]) -> Vec<VariableProblem> {
    let mut problems = Vec::new();
    for variable in &declared.variables {
        let stored: Vec<&VariableCell> = cells
            .iter()
            .filter(|cell| cell.key == variable.key)
            .collect();

        if variable.required {
            for folder in required_folders(variable) {
                if stored.iter().any(|cell| cell.folder == folder) {
                    continue;
                }
                if !owed(declared, variable, cells, folder) {
                    continue;
                }
                problems.push(problem(
                    variable.key,
                    folder,
                    Kind::KIND_MISSING,
                    String::new(),
                ));
            }
        }

        let Some(check) = variable.check else {
            continue;
        };
        for cell in stored {
            if let Err(said) = check(&cell.value) {
                problems.push(problem(
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

fn owed(
    declared: &Declared,
    variable: &DeclaredVariable,
    cells: &[VariableCell],
    folder: &str,
) -> bool {
    let Some(key) = variable.group else {
        return true;
    };
    let required = declared
        .groups
        .iter()
        .any(|group| group.key == key && group.required);
    required || switched_on(declared, key, cells, folder)
}

fn switched_on(declared: &Declared, key: &str, cells: &[VariableCell], folder: &str) -> bool {
    declared
        .variables
        .iter()
        .filter(|member| member.group == Some(key))
        .any(|member| stored_at(member, cells, folder))
}

fn stored_at(variable: &DeclaredVariable, cells: &[VariableCell], folder: &str) -> bool {
    let at = |where_: &str| {
        cells
            .iter()
            .any(|cell| cell.key == variable.key && cell.folder == where_)
    };
    if !variable.folders.is_empty() {
        return !folder.is_empty() && variable.folders.contains(&folder) && at(folder);
    }
    (!folder.is_empty() && at(folder)) || at("")
}

fn required_folders(variable: &DeclaredVariable) -> Vec<&str> {
    if variable.folders.is_empty() {
        return vec![""];
    }
    variable.folders.to_vec()
}

fn problem(key: &str, folder: &str, kind: Kind, detail: String) -> VariableProblem {
    VariableProblem {
        key: key.to_string(),
        folder: folder.to_string(),
        kind: kind.into(),
        detail,
        ..Default::default()
    }
}

fn request(resource: &DeclaredResource) -> DeclareRequest {
    DeclareRequest {
        resource: ResourceIdentifier {
            r#type: ResourceType::RESOURCE_TYPE_POSTGRES.into(),
            name: resource.name.to_string(),
            ..Default::default()
        }
        .into(),
        config: Some(Config::from(PostgresConfig {
            version: resource.version.to_string(),
            ..Default::default()
        })),
        source: source(resource.file, resource.line),
        ..Default::default()
    }
}

fn source(file: &str, line: u32) -> String {
    let path = std::path::Path::new(file);
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        source_root().join(path)
    };
    format!("{}:{}", absolute.display(), line)
}

fn source_root() -> std::path::PathBuf {
    match std::env::var(SOURCE_ROOT_ENV) {
        Ok(root) if !root.is_empty() => std::path::PathBuf::from(root),
        _ => std::env::current_dir().unwrap_or_default(),
    }
}

fn failed(resource: &DeclaredResource, said: String) -> Error {
    Error::Declare {
        kind: KIND.to_string(),
        name: resource.name.to_string(),
        said,
    }
}

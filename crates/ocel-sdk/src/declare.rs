use crate::env::{complaint, Class};
use crate::kv::{KvEntryDeclaration, ParsedPattern};
use crate::proto::app::resources::v1::declare_request::Config;
use crate::proto::app::resources::v1::variable_problem::Kind;
use crate::proto::app::resources::v1::ResourceType;
use crate::proto::app::resources::v1::{
    BatchPolicy, BucketConfig, ConsumerConfig, DeclareEnvRequest, DeclareRequest, GroupDefinition,
    KvConfig, KvEntry, PostgresConfig, ReportEnvProblemsRequest, ResourceIdentifier,
    ResourceServiceClient, RetryPolicy, TaskConfig, TopicConfig, VariableCell, VariableClass,
    VariableDefinition, VariableProblem, WorkerConfig,
};
#[cfg(feature = "realtime")]
use crate::proto::app::resources::v1::{
    RealtimeChannel, RealtimeConfig, RealtimePublish, RealtimeSubscribe,
};
#[cfg(feature = "realtime")]
use crate::realtime::{list_declared_channels, resolve_token_ttl, DeclaredChannel};
use crate::run::BoxFuture;
use crate::worker::{OnStart, WorkerMiddleware};
use crate::{Error, Lane};
use std::time::Duration;

const PHASE_ENV: &str = "OCEL_PHASE";
const DEV_SERVER_ENV: &str = "OCEL_DEV_SERVER";
const DEV_SERVER_TOKEN_ENV: &str = "OCEL_DEV_SERVER_TOKEN";
const SOURCE_ROOT_ENV: &str = "OCEL_SOURCE_ROOT";
const DISCOVERY_PHASE: &str = "discovery";
const SDK_VERSION_HEADER: &str = "ocel-sdk-version";
const SDK_VERSION: &str = concat!("rust/", env!("CARGO_PKG_VERSION"));

#[doc(hidden)]
#[derive(Clone, Copy)]
pub struct Retry {
    pub max_attempts: i32,
    pub min_delay: Option<Duration>,
    pub max_delay: Option<Duration>,
}

#[doc(hidden)]
#[derive(Clone, Copy)]
pub struct Batch {
    pub size: i32,
    pub timeout: Option<Duration>,
}

#[doc(hidden)]
pub type Schema = fn() -> String;

#[doc(hidden)]
pub type DeliveryFn = fn(crate::deliver::Delivery) -> BoxFuture<'static, crate::deliver::Outcome>;

#[doc(hidden)]
pub enum DeclaredConfig {
    Postgres {
        version: &'static str,
    },
    Bucket {
        public: bool,
        allowed_origins: &'static [&'static str],
    },
    Topic {
        schema: Option<Schema>,
        ordered: bool,
        retry: Option<Retry>,
    },
    Task {
        schema: Option<Schema>,
        ordered: bool,
        retry: Option<Retry>,
        concurrency: i32,
        max_duration: Option<Duration>,
        ttl: Option<Duration>,
        batch: Option<Batch>,
        worker: &'static str,
        cron: &'static str,
        deliver: DeliveryFn,
    },
    Consumer {
        topic: &'static str,
        worker: &'static str,
        retry: Option<Retry>,
        concurrency: i32,
        max_duration: Option<Duration>,
        lanes: &'static [Lane],
        batch: Option<Batch>,
        deliver: DeliveryFn,
    },
    Worker {
        concurrency: i32,
        on_start: Option<OnStart>,
        middleware: Option<WorkerMiddleware>,
    },
    Kv {
        version: &'static str,
        eviction: &'static str,
        memory: &'static str,
        entries: Vec<KvEntryDeclaration>,
    },
    #[cfg(feature = "realtime")]
    Realtime {
        channels: Vec<DeclaredChannel>,
        token_ttl: Duration,
    },
}

impl DeclaredConfig {
    fn kind(&self) -> &'static str {
        match self {
            Self::Postgres { .. } => crate::postgres::KIND,
            Self::Bucket { .. } => crate::bucket::KIND,
            Self::Topic { .. } => "topic",
            Self::Task { .. } => "task",
            Self::Consumer { .. } => "consumer",
            Self::Worker { .. } => "worker",
            Self::Kv { .. } => "kv",
            #[cfg(feature = "realtime")]
            Self::Realtime { .. } => "realtime",
        }
    }

    fn namespace(&self) -> &'static str {
        match self {
            Self::Postgres { .. } | Self::Bucket { .. } | Self::Kv { .. } => "resource",
            #[cfg(feature = "realtime")]
            Self::Realtime { .. } => "resource",
            Self::Topic { .. } | Self::Task { .. } => "topic",
            Self::Consumer { .. } => "consumer",
            Self::Worker { .. } => "worker",
        }
    }

    fn claimed_name(&self, name: &str) -> String {
        match self {
            Self::Consumer { topic, .. } => format!("{topic}/{name}"),
            _ => name.to_string(),
        }
    }

    fn resource_type(&self) -> ResourceType {
        match self {
            Self::Postgres { .. } => ResourceType::RESOURCE_TYPE_POSTGRES,
            Self::Bucket { .. } => ResourceType::RESOURCE_TYPE_BUCKET,
            Self::Topic { .. } => ResourceType::RESOURCE_TYPE_TOPIC,
            Self::Task { .. } => ResourceType::RESOURCE_TYPE_TASK,
            Self::Consumer { .. } => ResourceType::RESOURCE_TYPE_CONSUMER,
            Self::Worker { .. } => ResourceType::RESOURCE_TYPE_WORKER,
            Self::Kv { .. } => ResourceType::RESOURCE_TYPE_KV,
            #[cfg(feature = "realtime")]
            Self::Realtime { .. } => ResourceType::RESOURCE_TYPE_REALTIME,
        }
    }

    fn config(&self) -> Config {
        match self {
            Self::Postgres { version } => Config::from(PostgresConfig {
                version: version.to_string(),
                ..Default::default()
            }),
            Self::Bucket {
                public,
                allowed_origins,
            } => Config::from(BucketConfig {
                public: *public,
                allowed_origins: allowed_origins.iter().map(|one| one.to_string()).collect(),
                ..Default::default()
            }),
            Self::Topic {
                schema,
                ordered,
                retry,
            } => Config::from(TopicConfig {
                schema: schema.map(|schema| schema()).unwrap_or_default(),
                ordered: *ordered,
                retry: retry.map(build_retry_policy).into(),
                ..Default::default()
            }),
            Self::Task {
                schema,
                ordered,
                retry,
                concurrency,
                max_duration,
                ttl,
                batch,
                worker,
                cron,
                deliver: _,
            } => Config::from(TaskConfig {
                schema: schema.map(|schema| schema()).unwrap_or_default(),
                ordered: *ordered,
                retry: retry.map(build_retry_policy).into(),
                concurrency: *concurrency,
                max_duration: max_duration.map(Into::into).into(),
                ttl: ttl.map(Into::into).into(),
                batch: batch.map(build_batch_policy).into(),
                worker: worker.to_string(),
                cron: cron.to_string(),
                ..Default::default()
            }),
            Self::Consumer {
                topic,
                worker,
                retry,
                concurrency,
                max_duration,
                lanes,
                batch,
                deliver: _,
            } => Config::from(ConsumerConfig {
                topic: topic.to_string(),
                worker: worker.to_string(),
                retry: retry.map(build_retry_policy).into(),
                concurrency: *concurrency,
                max_duration: max_duration.map(Into::into).into(),
                lanes: lanes.iter().map(|lane| lane.to_wire().into()).collect(),
                batch: batch.map(build_batch_policy).into(),
                ..Default::default()
            }),
            Self::Worker { concurrency, .. } => Config::from(WorkerConfig {
                concurrency: *concurrency,
                ..Default::default()
            }),
            Self::Kv {
                version,
                eviction,
                memory,
                entries,
            } => Config::from(KvConfig {
                version: version.to_string(),
                eviction: eviction.to_string(),
                memory: memory.to_string(),
                entries: entries
                    .iter()
                    .map(|entry| KvEntry {
                        name: entry.name.to_string(),
                        pattern: entry.pattern.to_string(),
                        shape: entry.shape.to_wire().into(),
                        source: format_source(entry.file, entry.line),
                        ..Default::default()
                    })
                    .collect(),
                ..Default::default()
            }),
            #[cfg(feature = "realtime")]
            Self::Realtime {
                channels,
                token_ttl,
            } => Config::from(RealtimeConfig {
                channels: channels
                    .iter()
                    .map(|channel| RealtimeChannel {
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
                    })
                    .collect(),
                token_ttl: buffa::MessageField::some((*token_ttl).into()),
                ..Default::default()
            }),
        }
    }
}

fn build_retry_policy(retry: Retry) -> RetryPolicy {
    RetryPolicy {
        max_attempts: retry.max_attempts,
        min_delay: retry.min_delay.map(Into::into).into(),
        max_delay: retry.max_delay.map(Into::into).into(),
        ..Default::default()
    }
}

fn build_batch_policy(batch: Batch) -> BatchPolicy {
    BatchPolicy {
        size: batch.size,
        timeout: batch.timeout.map(Into::into).into(),
        ..Default::default()
    }
}

#[doc(hidden)]
pub struct DeclaredResource {
    pub name: &'static str,
    pub config: DeclaredConfig,
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

pub(crate) fn collect_declarations() -> Result<Declared, Error> {
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
    let mut resource_owners: Vec<(String, String)> = Vec::new();
    let mut variable_owners: Vec<(&str, String)> = Vec::new();
    let mut group_owners: Vec<(&str, String)> = Vec::new();

    for one in structs {
        for resource in one.resources {
            claim_resource(&mut resource_owners, &resource)?;
            refuse_overlapping_entries(&resource)?;
            all.resources.push(resource);
        }
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
    for resource in collect_realtime_resources()? {
        claim_resource(&mut resource_owners, &resource)?;
        all.resources.push(resource);
    }
    join_group_members(&mut all)?;
    Ok(all)
}

#[cfg(feature = "realtime")]
fn collect_realtime_resources() -> Result<Vec<DeclaredResource>, Error> {
    let mut resources: Vec<DeclaredResource> = Vec::new();
    for channel in list_declared_channels() {
        let resource = match resources
            .iter_mut()
            .find(|resource| resource.name == channel.realtime)
        {
            Some(resource) => resource,
            None => {
                resources.push(DeclaredResource {
                    name: channel.realtime,
                    config: DeclaredConfig::Realtime {
                        channels: Vec::new(),
                        token_ttl: Duration::ZERO,
                    },
                    file: channel.file,
                    line: channel.line,
                });
                resources.last_mut().expect("the resource just pushed")
            }
        };
        let DeclaredConfig::Realtime { channels, .. } = &mut resource.config else {
            unreachable!("a realtime resource holds realtime channels");
        };
        if let Some(prior) = channels
            .iter()
            .find(|prior| prior.pattern == channel.pattern)
        {
            return Err(Error::Definition {
                key: channel.realtime.to_string(),
                detail: format!(
                    "declares channel \"{}\" at {} and at {}, and a realtime resource declares each pattern once",
                    channel.pattern,
                    format_site(prior.file, prior.line),
                    format_site(channel.file, channel.line)
                ),
            });
        }
        channels.push(channel);
    }
    for resource in &mut resources {
        let DeclaredConfig::Realtime {
            channels,
            token_ttl,
        } = &mut resource.config
        else {
            unreachable!("a realtime resource holds realtime channels");
        };
        *token_ttl = resolve_token_ttl(channels).map_err(|detail| Error::Definition {
            key: resource.name.to_string(),
            detail,
        })?;
    }
    Ok(resources)
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

fn claim_resource(
    owners: &mut Vec<(String, String)>,
    resource: &DeclaredResource,
) -> Result<(), Error> {
    let name = resource.config.claimed_name(resource.name);
    let key = format!("{}:{name}", resource.config.namespace());
    let site = format_site(resource.file, resource.line);
    if let Some((_, claimed)) = owners.iter().find(|(seen, _)| *seen == key) {
        let rule = match resource.config.namespace() {
            "topic" => "Topics and tasks share one namespace, and a name in it is declared exactly once, in exactly one file.",
            "consumer" => "A topic's consumer is declared exactly once, in exactly one file.",
            "worker" => "A worker is declared exactly once, in exactly one file.",
            _ => "A resource name is declared exactly once, in exactly one file.",
        };
        return Err(Error::Definition {
            key: name,
            detail: format!("is declared in {claimed} and {site}. {rule}"),
        });
    }
    owners.push((key, site));
    Ok(())
}

fn refuse_overlapping_entries(resource: &DeclaredResource) -> Result<(), Error> {
    let DeclaredConfig::Kv { entries, .. } = &resource.config else {
        return Ok(());
    };
    for (index, entry) in entries.iter().enumerate() {
        for prior in &entries[..index] {
            let (site, prior_site) = (
                format_site(entry.file, entry.line),
                format_site(prior.file, prior.line),
            );
            let detail = if prior.name == entry.name {
                format!(
                    "has entry '{}' declared at {prior_site} and at {site}, and a store names each entry once",
                    entry.name
                )
            } else if ParsedPattern::parse(prior.pattern)
                .overlaps(&ParsedPattern::parse(entry.pattern))
            {
                format!(
                    "has entry '{}' declared at {site} with pattern \"{}\", which overlaps pattern \"{}\" of entry '{}' declared at {prior_site}: some key would match both, so neither entry could tell its keys from the other's",
                    entry.name, entry.pattern, prior.pattern, prior.name
                )
            } else {
                continue;
            };
            return Err(Error::Definition {
                key: resource.name.to_string(),
                detail,
            });
        }
    }
    Ok(())
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
        let stored: Vec<&VariableCell> = cells
            .iter()
            .filter(|cell| cell.key == variable.key)
            .collect();

        if variable.required {
            for folder in list_required_folders(variable) {
                if stored.iter().any(|cell| cell.folder == folder) {
                    continue;
                }
                if !is_required_at(declared, variable, cells, folder) {
                    continue;
                }
                problems.push(new_problem(
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

fn is_required_at(
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
    required || is_group_switched_on(declared, key, cells, folder)
}

fn is_group_switched_on(
    declared: &Declared,
    key: &str,
    cells: &[VariableCell],
    folder: &str,
) -> bool {
    declared
        .variables
        .iter()
        .filter(|member| member.group == Some(key))
        .any(|member| has_cell(member, cells, folder))
}

fn has_cell(variable: &DeclaredVariable, cells: &[VariableCell], folder: &str) -> bool {
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

fn list_required_folders(variable: &DeclaredVariable) -> Vec<&str> {
    if variable.folders.is_empty() {
        return vec![""];
    }
    variable.folders.to_vec()
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
        resource: ResourceIdentifier {
            r#type: resource.config.resource_type().into(),
            name: resource.name.to_string(),
            ..Default::default()
        }
        .into(),
        config: Some(resource.config.config()),
        source: format_source(resource.file, resource.line),
        ..Default::default()
    }
}

fn format_source(file: &str, line: u32) -> String {
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
    Error::Declare {
        kind: resource.config.kind().to_string(),
        name: resource.name.to_string(),
        said,
    }
}

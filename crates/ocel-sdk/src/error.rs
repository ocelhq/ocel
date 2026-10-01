fn bound_to(binding: &str) -> &str {
    if binding.is_empty() {
        "the project root"
    } else {
        binding
    }
}

/// Everything a declared resource can fail with.
#[derive(Debug, thiserror::Error)]
#[non_exhaustive]
pub enum Error {
    /// App code reached for a resource this run never provisioned, which is discovery:
    /// the pass that reads the declarations before anything is provisioned.
    #[error("'{resource}' cannot be used during discovery: tried to access '{access}' before the resource was provisioned")]
    Unprovisioned {
        /// The declaration the accessor belongs to, as written in code.
        resource: String,
        /// The accessor that was called.
        access: String,
    },

    /// No binding was delivered to the process for the declared name.
    #[error("Value for {key} is not defined. Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered from the resource this app binds.")]
    MissingBinding {
        /// The environment variable the binding arrives in.
        key: String,
    },

    /// A binding was delivered, but it is for another kind of resource.
    #[error("{key} contains a {found} binding, and this app reads it as a {expected}")]
    WrongBindingType {
        /// The environment variable the binding arrived in.
        key: String,
        /// The kind of resource the delivered binding is for.
        found: String,
        /// The kind of resource the app read it as.
        expected: String,
    },

    /// The environment variable contains something that is not a binding record.
    #[error("{key} does not contain a binding record, so this app cannot read it as a {expected}")]
    Binding {
        /// The environment variable the binding arrived in.
        key: String,
        /// The kind of resource the app read it as.
        expected: String,
    },

    /// The address discovery was told to post declarations to is not a URL.
    #[error("ocel: OCEL_DEV_SERVER is not a URL discovery can post to: '{server}'")]
    DevServer {
        /// The address the environment set.
        server: String,
    },

    /// Discovery could not tell the CLI about a declaration.
    #[error("ocel: declare {kind} '{name}': {said}")]
    Declare {
        /// The kind of resource being declared.
        kind: String,
        /// The name it is declared under.
        name: String,
        /// What the dev server said.
        said: String,
    },

    /// The process was run as a worker and could not listen for its deliveries.
    #[error("ocel: worker '{worker}' cannot listen on {address}: {said}")]
    Listen {
        /// The worker the process was run as.
        worker: String,
        /// The address it was told to listen on.
        address: String,
        /// Why it could not.
        said: String,
    },

    /// Discovery could not tell the CLI about the variables a struct declares.
    #[error("ocel: declare env: {said}")]
    DeclareEnv {
        /// What the dev server said.
        said: String,
    },

    /// Two files declare the same key or the same resource name.
    #[error("'{key}' {detail}")]
    Definition {
        /// The key or name that is declared twice.
        key: String,
        /// What is wrong and how to fix it.
        detail: String,
    },

    /// A variable is scoped to folders this app is not bound to.
    #[error("'{key}' is scoped to {}, but this app is bound to {}. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope.", .folders.join(", "), bound_to(.binding))]
    Scope {
        /// The scoped variable.
        key: String,
        /// The scope the variable was declared with.
        folders: Vec<String>,
        /// The folder this app is bound to, empty at the project root.
        binding: String,
    },

    /// A declared variable has no value.
    #[error("'{key}' has no value. Set one with `ocel env set {key}=<VALUE>`.")]
    Unset {
        /// The variable the value belongs to.
        key: String,
    },

    /// A declared variable has a value its field type rejects.
    #[error("'{key}' is set but does not satisfy its type: {detail}. Fix it with `ocel env set {key}=<VALUE>`.")]
    Invalid {
        /// The variable the value belongs to.
        key: String,
        /// What the parse said, withheld for a confidential class.
        detail: String,
    },

    /// The deploy gave this app no hostname to serve on.
    #[error("'{key}' was not delivered to this app. Ocel writes it from the hostname the deploy serves the app on, and this app is served on none: add one under `domains.production` on the app, or on the project if this is the first app it names, and deploy again.")]
    Undelivered {
        /// The variable Ocel writes the url into.
        key: String,
    },

    /// An operation that cannot answer with nothing named an object the bucket does not
    /// have.
    #[error("the bucket has no object under '{key}'")]
    NotFound {
        /// The key that named nothing.
        key: String,
    },

    /// A write set `if_not_exists` or `if_match`, and the object did not meet it.
    #[error("the object under '{key}' did not meet the condition this write set")]
    PreconditionFailed {
        /// The key whose current state refused the write.
        key: String,
    },

    /// The store refused an operation on an object.
    #[error("'{key}' was refused by the store: {said}")]
    Refused {
        /// The key the operation names.
        key: String,
        /// What the store or the runtime said.
        said: String,
    },

    /// Nothing told this app where the runtime that serves its resources listens.
    #[error("OCEL_RUNTIME_ADDRESS is not defined, so no resource the ocel runtime serves can be reached. Run `ocel dev` to serve it locally, or `ocel deploy` to have the deployed runtime's address delivered.")]
    UnreachableRuntime,

    /// The address the runtime was said to listen on is not a URL.
    #[error("ocel: OCEL_RUNTIME_ADDRESS is not a URL the runtime can be reached at: '{address}'")]
    RuntimeAddress {
        /// The address the environment set.
        address: String,
    },

    /// No session token was delivered, so every call to the runtime would be refused.
    #[error("OCEL_SESSION_TOKEN is not defined, so the ocel runtime at OCEL_RUNTIME_ADDRESS would refuse every call. It is delivered beside the address by `ocel dev` and by the deployed runtime, never set by hand.")]
    UntrustedRuntime,

    /// A public url was asked of a bucket that is served at no public address.
    #[error("this bucket has no public address, so '{key}' has no public url: declare the bucket with #[ocel(public)] and give the project a domain to serve it from")]
    NotPublic {
        /// The key a public url was asked for.
        key: String,
    },

    /// A payload could not be written as JSON.
    #[error("the payload cannot be sent, because it does not encode as JSON: {said}")]
    Payload {
        /// What the encoder said.
        said: String,
    },

    /// A payload's JSON is larger than any topic or task accepts.
    #[error(
        "the payload is {size} bytes of JSON, and a payload is at most 262144 bytes (256 KiB)"
    )]
    PayloadTooLarge {
        /// The size of the payload's JSON in bytes.
        size: usize,
    },

    /// The runtime refused an operation on a topic, a task or its runs.
    #[error("'{resource}' {access} was refused by the runtime: {said}")]
    RuntimeRefused {
        /// The declaration or the runs the operation names.
        resource: String,
        /// The operation that was refused.
        access: String,
        /// What the runtime said.
        said: String,
    },

    /// No run has the id an operation named.
    #[error("no run has the id '{id}'")]
    UnknownRun {
        /// The id that named nothing.
        id: String,
    },

    /// A batch contains a trigger built from another task.
    #[error("a batch triggering '{task}' contains a trigger of '{other}': every trigger in a batch is built from the task the batch is sent to")]
    MixedBatch {
        /// The task the batch is sent to.
        task: String,
        /// The task the stray trigger was built from.
        other: String,
    },

    /// A value read from a kv entry, or written to one, is not a value of the entry's
    /// shape: json that does not decode into the entry's type, or a counter that holds no
    /// integer.
    #[error("kv key '{key}' {reason}")]
    InvalidKvValue {
        /// The key whose value is invalid.
        key: String,
        /// What is wrong with the value.
        reason: String,
    },

    /// A write to a kv entry asked for a TTL shorter than the millisecond a store counts in.
    #[error("a kv ttl is at least 1ms, and {ttl:?} is shorter")]
    InvalidKvTtl {
        /// The TTL the write asked for.
        ttl: std::time::Duration,
    },

    /// A key reached a kv store whose `entries` do not list the key's type, so the store
    /// never declared the key's pattern.
    #[error("kv store '{store}' does not list {key} in its entries: add it to entries = [...]")]
    UndeclaredKvEntry {
        /// The store the key reached.
        store: String,
        /// The key's type.
        key: String,
    },

    /// A write to a list or set entry was given no values, which no store command takes.
    #[error("{access} takes one or more values, and was given none")]
    EmptyKvWrite {
        /// The entry and operation that was called, as `entry.operation`.
        access: String,
    },

    /// A kv binding was delivered with a port no TCP connection can be made to.
    #[error("{key} delivers port {port} for its kv store, and a port is 1 to 65535")]
    InvalidKvPort {
        /// The environment variable the binding arrived in.
        key: String,
        /// The port the binding delivered.
        port: i32,
    },

    #[cfg(feature = "realtime")]
    /// A realtime resource's name, token TTL or rules disagree with the contract or with
    /// the channels declared under its name.
    #[error("ocel: realtime '{name}': {detail}")]
    RealtimeDeclaration {
        /// The name the resource was built under.
        name: String,
        /// What is wrong and how to fix it.
        detail: String,
    },

    #[cfg(feature = "realtime")]
    /// A publish named a channel, params or an event the realtime resource cannot carry.
    #[error("realtime channel '{pattern}' refuses the publish: {code}")]
    PublishRefused {
        /// The channel pattern published on.
        pattern: String,
        /// The reason: `unknown-pattern` for a channel of another realtime resource,
        /// `empty-value` or `value-too-long` for a param, `invalid-body` for an event the
        /// channel's JSON Schema refuses, or `body-too-large` for an event over 240 KiB.
        code: crate::realtime::DenialCode,
    },

    #[cfg(feature = "realtime")]
    /// The transport refused a publish, could not be reached, or did not answer within 10
    /// seconds.
    #[error("ocel: realtime '{name}': {said}")]
    PublishFailed {
        /// The realtime resource published on.
        name: String,
        /// What went wrong.
        said: String,
    },

    /// A kv store refused an operation, or could not be reached.
    #[cfg(feature = "kv")]
    #[error("{0}")]
    Kv(#[from] redis::RedisError),

    /// The pool over a delivered binding could not be opened.
    #[cfg(feature = "postgres")]
    #[error("{0}")]
    Pool(#[from] sqlx::Error),
}

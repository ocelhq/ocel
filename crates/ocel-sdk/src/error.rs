fn bound_to(binding: &str) -> &str {
    if binding.is_empty() {
        "the project root"
    } else {
        binding
    }
}

/// Everything a declared resource can fail with. Each message starts lowercase, unless it
/// opens with a name, and ends without punctuation.
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
    #[error("{key} is not delivered to this process: `ocel dev` delivers it locally and `ocel deploy` to the deployed app, and a build gets it only from an `ocel deploy` whose provider forwards a port to the resource, so code that runs while building, such as prerendering a page, cannot use it under `ocel build`")]
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
    #[error("OCEL_DEV_SERVER is not a URL discovery can post to: '{server}'")]
    DevServer {
        /// The address the environment set.
        server: String,
    },

    /// Discovery could not tell the CLI about a declaration.
    #[error("declare {kind} '{name}': {said}")]
    Declare {
        /// The kind of resource being declared.
        kind: String,
        /// The name it is declared under.
        name: String,
        /// What the dev server said.
        said: String,
    },

    /// The process was run as a worker and could not listen for its deliveries.
    #[error("worker '{worker}' cannot listen on {address}: {said}")]
    Listen {
        /// The worker the process was run as.
        worker: String,
        /// The address it was told to listen on.
        address: String,
        /// Why it could not.
        said: String,
    },

    /// Discovery could not tell the CLI about the variables a struct declares.
    #[error("declare env: {said}")]
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
    #[error("'{key}' is scoped to {}, but this app is bound to {}: bind this app to one of those folders in ocel.json, or widen the variable's scope", .folders.join(", "), bound_to(.binding))]
    Scope {
        /// The scoped variable.
        key: String,
        /// The scope the variable was declared with.
        folders: Vec<String>,
        /// The folder this app is bound to, empty at the project root.
        binding: String,
    },

    /// A declared variable has no value.
    #[error("'{key}' has no value: set one with `ocel env set {key}=<VALUE>`")]
    Unset {
        /// The variable the value belongs to.
        key: String,
    },

    /// A declared variable has a value its field type rejects.
    #[error("'{key}' is set but does not satisfy its type: {detail}; fix it with `ocel env set {key}=<VALUE>`")]
    Invalid {
        /// The variable the value belongs to.
        key: String,
        /// What the parse said, withheld for a confidential class.
        detail: String,
    },

    /// The deploy gave this app no hostname to serve on.
    #[error("'{key}' was not delivered to this app: Ocel writes it from the hostname the deploy serves the app on, and this app is served on none, so add one under `domains.production` on the app, or on the project if this is the first app it names, and deploy again")]
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

    /// Another write replaced the object between learning what it was and reading its
    /// bytes, on every attempt a read made, so no read answered one version's bytes beside
    /// that same version's info.
    #[error("the object under '{key}' changed while it was read")]
    Changed {
        /// The key whose object kept changing.
        key: String,
    },

    /// The store, or the runtime in front of it, refused an operation on an object or
    /// could not be reached.
    #[error("'{key}' was refused by the store: {said}")]
    Refused {
        /// The key the operation names.
        key: String,
        /// How the refusal is classed, which says whether the same call can succeed later.
        code: Code,
        /// What the store or the runtime said.
        said: String,
    },

    /// Nothing told this app where the runtime that serves its resources listens.
    #[error("OCEL_RUNTIME_ADDRESS is not defined, so no resource the ocel runtime serves can be reached: run `ocel dev` to serve it locally, or `ocel deploy` to have the deployed runtime's address delivered")]
    UnreachableRuntime,

    /// The address the runtime was said to listen on is not a URL.
    #[error("OCEL_RUNTIME_ADDRESS is not a URL the runtime can be reached at: '{address}'")]
    RuntimeAddress {
        /// The address the environment set.
        address: String,
    },

    /// No session token was delivered, so every call to the runtime would be refused.
    #[error("OCEL_SESSION_TOKEN is not defined, so the ocel runtime at OCEL_RUNTIME_ADDRESS would refuse every call: it is delivered beside the address by `ocel dev` and by the deployed runtime, never set by hand")]
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

    /// The runtime refused an operation on a topic, a task or its runs, or could not be
    /// reached.
    #[error("'{resource}' {access} was refused by the runtime: {said}")]
    RuntimeRefused {
        /// The declaration or the runs the operation names.
        resource: String,
        /// The operation that was refused.
        access: String,
        /// How the refusal is classed, which says whether the same call can succeed later.
        code: Code,
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
    #[error("realtime '{name}': {detail}")]
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
    /// The ocel runtime did not publish the event: it could not be reached, or the
    /// transport behind it refused the event.
    #[error("realtime '{name}': {said}")]
    PublishFailed {
        /// The realtime resource published on.
        name: String,
        /// How the failure is classed, which says whether the same publish can succeed
        /// later.
        code: Code,
        /// What went wrong.
        said: String,
    },

    /// A kv binding was delivered with a certificate authority that holds no PEM
    /// certificate, so no client could tell the store's certificate from a forged one.
    #[cfg(feature = "kv")]
    #[error("{key} delivers a caPem for its kv store that holds no PEM certificate")]
    InvalidKvAuthority {
        /// The environment variable the binding arrived in.
        key: String,
    },

    /// A kv store refused an operation, or could not be reached.
    #[cfg(feature = "kv")]
    #[error("the kv store refused the operation: {source}")]
    Kv {
        /// The kv client's own error, which a caller that depends on the same client can
        /// downcast.
        source: Box<dyn std::error::Error + Send + Sync>,
    },

    /// The pool over a delivered binding could not be opened.
    #[cfg(feature = "postgres")]
    #[error("the postgres pool could not be opened: {source}")]
    Pool {
        /// The postgres driver's own error, which a caller that depends on the same driver
        /// can downcast.
        source: Box<dyn std::error::Error + Send + Sync>,
    },
}

impl Error {
    /// How the refusal behind this error is classed, for the errors a runtime, a store or a
    /// realtime transport answers with: [`Error::RuntimeRefused`], [`Error::Refused`] and
    /// [`Error::PublishFailed`]. Every other error answers `None`.
    pub fn code(&self) -> Option<Code> {
        match self {
            Self::RuntimeRefused { code, .. } | Self::Refused { code, .. } => Some(*code),
            #[cfg(feature = "realtime")]
            Self::PublishFailed { code, .. } => Some(*code),
            _ => None,
        }
    }

    /// Whether the same call, made again later, can succeed: true only when the error's
    /// [`code`](Self::code) [is retryable](Code::is_retryable).
    pub fn is_retryable(&self) -> bool {
        self.code().is_some_and(Code::is_retryable)
    }

    #[cfg(feature = "kv")]
    pub(crate) fn from_kv(err: redis::RedisError) -> Self {
        Self::Kv {
            source: Box::new(err),
        }
    }

    #[cfg(feature = "postgres")]
    pub(crate) fn from_pool(err: sqlx::Error) -> Self {
        Self::Pool {
            source: Box::new(err),
        }
    }
}

/// How a refusal is classed, in the codes of the Connect protocol the ocel runtime speaks.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[non_exhaustive]
pub enum Code {
    /// The call was canceled, usually by its caller.
    Canceled,
    /// The refusal fits no other code.
    Unknown,
    /// The call named something invalid, whatever the state of the system.
    InvalidArgument,
    /// The call did not finish before its deadline.
    DeadlineExceeded,
    /// Something the call named does not exist.
    NotFound,
    /// Something the call would create already exists.
    AlreadyExists,
    /// The caller may not make this call.
    PermissionDenied,
    /// A quota or a rate limit was reached.
    ResourceExhausted,
    /// The system is not in the state the call requires.
    FailedPrecondition,
    /// The call was aborted, usually by a conflict with another.
    Aborted,
    /// The call named a position past the end of what it reads.
    OutOfRange,
    /// The runtime or the store does not serve this call.
    Unimplemented,
    /// The runtime broke one of its own invariants.
    Internal,
    /// The runtime or the store could not be reached, or could not serve the call now.
    Unavailable,
    /// Data was lost or corrupted.
    DataLoss,
    /// The call carried no valid credential.
    Unauthenticated,
}

impl Code {
    /// Whether a call refused with this code can succeed if made again later: true for
    /// [`Unavailable`](Self::Unavailable), [`DeadlineExceeded`](Self::DeadlineExceeded),
    /// [`ResourceExhausted`](Self::ResourceExhausted) and [`Aborted`](Self::Aborted).
    pub fn is_retryable(self) -> bool {
        matches!(
            self,
            Self::Unavailable | Self::DeadlineExceeded | Self::ResourceExhausted | Self::Aborted
        )
    }

    pub(crate) fn from_connect(code: connectrpc::ErrorCode) -> Self {
        use connectrpc::ErrorCode;
        match code {
            ErrorCode::Canceled => Self::Canceled,
            ErrorCode::InvalidArgument => Self::InvalidArgument,
            ErrorCode::DeadlineExceeded => Self::DeadlineExceeded,
            ErrorCode::NotFound => Self::NotFound,
            ErrorCode::AlreadyExists => Self::AlreadyExists,
            ErrorCode::PermissionDenied => Self::PermissionDenied,
            ErrorCode::ResourceExhausted => Self::ResourceExhausted,
            ErrorCode::FailedPrecondition => Self::FailedPrecondition,
            ErrorCode::Aborted => Self::Aborted,
            ErrorCode::OutOfRange => Self::OutOfRange,
            ErrorCode::Unimplemented => Self::Unimplemented,
            ErrorCode::Internal => Self::Internal,
            ErrorCode::Unavailable => Self::Unavailable,
            ErrorCode::DataLoss => Self::DataLoss,
            ErrorCode::Unauthenticated => Self::Unauthenticated,
            _ => Self::Unknown,
        }
    }

    pub(crate) fn from_store_status(status: u16) -> Self {
        match status {
            400 => Self::InvalidArgument,
            401 => Self::Unauthenticated,
            403 => Self::PermissionDenied,
            408 => Self::DeadlineExceeded,
            429 => Self::ResourceExhausted,
            501 => Self::Unimplemented,
            500 | 502 | 503 | 504 => Self::Unavailable,
            _ => Self::Unknown,
        }
    }
}

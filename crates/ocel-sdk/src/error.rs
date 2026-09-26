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

    /// The pool over a delivered binding could not be opened.
    #[cfg(feature = "postgres")]
    #[error("{0}")]
    Pool(#[from] sqlx::Error),
}

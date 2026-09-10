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
    /// the pass that reads the declarations before anything stands.
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

    /// A binding was delivered, but it carries another kind of resource.
    #[error("{key} carries a {carried} binding, and this app reads it as a {expected}")]
    WrongBindingType {
        /// The environment variable the binding arrived in.
        key: String,
        /// The kind of resource the delivered binding carries.
        carried: String,
        /// The kind of resource the app read it as.
        expected: String,
    },

    /// The environment variable holds something that is not a binding record.
    #[error("{key} does not carry a binding record, so this app cannot read it as a {expected}")]
    Binding {
        /// The environment variable the binding arrived in.
        key: String,
        /// The kind of resource the app read it as.
        expected: String,
    },

    /// The address discovery was told to post declarations to is not a URL.
    #[error("ocel: OCEL_DEV_SERVER does not hold a URL discovery can post to: '{server}'")]
    DevServer {
        /// The address the environment carried.
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

    /// The pool over a delivered binding could not be opened.
    #[cfg(feature = "postgres")]
    #[error("{0}")]
    Pool(#[from] sqlx::Error),
}

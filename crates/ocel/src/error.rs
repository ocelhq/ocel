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

    /// No link was delivered to the process for the declared name.
    #[error("Value for {key} is not defined. Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered from the resource this app links.")]
    MissingLink {
        /// The environment variable the link arrives in.
        key: String,
    },

    /// A link was delivered, but it carries another kind of resource.
    #[error("{key} carries a {carried} link, and this app reads it as a {expected}")]
    WrongLinkType {
        /// The environment variable the link arrived in.
        key: String,
        /// The kind of resource the delivered link carries.
        carried: String,
        /// The kind of resource the app read it as.
        expected: String,
    },

    /// The environment variable holds something that is not a link record.
    #[error("{key} does not carry a link record, so this app cannot read it as a {expected}")]
    Link {
        /// The environment variable the link arrived in.
        key: String,
        /// The kind of resource the app read it as.
        expected: String,
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

    /// The pool over a delivered link could not be opened.
    #[cfg(feature = "postgres")]
    #[error("{0}")]
    Pool(#[from] sqlx::Error),
}

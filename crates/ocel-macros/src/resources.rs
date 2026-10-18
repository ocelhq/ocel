use crate::attribute::{parse_entries, read_container_crate_path, Entry};
use crate::options::{
    parse_count, parse_retry, quote_declared, quote_none, quote_resources, quote_unset,
};
use crate::source::source;
use crate::task::{quote_no_schema, quote_schema};
use proc_macro2::TokenStream;
use quote::{format_ident, quote, quote_spanned};
use syn::spanned::Spanned;
use syn::{Data, DeriveInput, Fields, GenericArgument, PathArguments, Type};

const DEFAULT_VERSION: &str = "17";

enum Config {
    Postgres {
        version: String,
    },
    Bucket {
        public: bool,
        allowed_origins: Vec<String>,
    },
    Topic {
        payload: Type,
        schema: TokenStream,
        ordered: bool,
        retry: TokenStream,
    },
    Worker {
        concurrency: i32,
        on_start: Option<syn::Path>,
        middleware: Option<syn::Path>,
    },
    Kv {
        version: String,
        eviction: String,
        memory: String,
        entries: Vec<syn::Path>,
    },
}

struct Resource {
    ident: syn::Ident,
    visibility: syn::Visibility,
    name: String,
    config: Config,
    file: String,
    line: u32,
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let krate = &read_container_crate_path(input, "ocel::Resources")?;
    let resources = read_resources(input, krate)?;
    let ident = &input.ident;

    let v1 = quote_resources(krate);
    let mut handlers = Vec::new();
    let declared: Vec<TokenStream> = resources
        .iter()
        .map(|resource| {
            let name = &resource.name;
            let site = (resource.file.as_str(), resource.line);
            match &resource.config {
                Config::Postgres { version } => quote_declared(
                    krate,
                    "POSTGRES",
                    name,
                    quote! {
                        #v1::PostgresConfig {
                            version: ::std::string::String::from(#version),
                            ..::core::default::Default::default()
                        }
                    },
                    site,
                ),
                Config::Bucket {
                    public,
                    allowed_origins,
                } => quote_declared(
                    krate,
                    "BUCKET",
                    name,
                    quote! {
                        #v1::BucketConfig {
                            public: #public,
                            allowed_origins: ::std::vec![#(::std::string::String::from(#allowed_origins)),*],
                            ..::core::default::Default::default()
                        }
                    },
                    site,
                ),
                Config::Topic {
                    schema,
                    ordered,
                    retry,
                    ..
                } => quote_declared(
                    krate,
                    "TOPIC",
                    name,
                    quote! {
                        #v1::TopicConfig {
                            schema: #schema,
                            ordered: #ordered,
                            retry: #retry,
                            ..::core::default::Default::default()
                        }
                    },
                    site,
                ),
                Config::Kv {
                    version,
                    eviction,
                    memory,
                    entries,
                } => quote_declared(
                    krate,
                    "KV",
                    name,
                    quote! {
                        #v1::KvConfig {
                            version: ::std::string::String::from(#version),
                            eviction: ::std::string::String::from(#eviction),
                            memory: ::std::string::String::from(#memory),
                            entries: ::std::vec![#(<#entries as #krate::KvKey>::ENTRY.to_proto()),*],
                            ..::core::default::Default::default()
                        }
                    },
                    site,
                ),
                Config::Worker {
                    concurrency,
                    on_start,
                    middleware,
                } => {
                    handlers.push(quote_worker_handler(
                        krate,
                        &resource.ident,
                        name,
                        *concurrency,
                        on_start.as_ref(),
                        middleware.as_ref(),
                    ));
                    quote_declared(
                        krate,
                        "WORKER",
                        name,
                        quote! {
                            #v1::WorkerConfig {
                                concurrency: #concurrency,
                                ..::core::default::Default::default()
                            }
                        },
                        site,
                    )
                }
            }
        })
        .collect();
    let fields = resources.iter().map(|resource| {
        let (field, name) = (&resource.ident, &resource.name);
        match &resource.config {
            Config::Postgres { .. } => quote!(#field: #krate::Postgres::new(#name)),
            Config::Bucket { .. } => quote!(#field: #krate::Bucket::new(#name)),
            Config::Topic { payload, .. } => {
                quote!(#field: #krate::Topic::<#payload>::new(#name))
            }
            Config::Worker { .. } => quote!(#field: #krate::Worker::new(#name)),
            Config::Kv { entries, .. } => {
                quote!(#field: #krate::Kv::new(#name)#(.with_entry::<#entries>())*)
            }
        }
    });

    let topic_names = resources.iter().filter_map(|resource| {
        let Config::Topic { payload, .. } = &resource.config else {
            return None;
        };
        let constant = format_ident!(
            "{}",
            resource
                .ident
                .to_string()
                .trim_start_matches("r#")
                .to_uppercase(),
            span = resource.ident.span()
        );
        let (visibility, name) = (&resource.visibility, &resource.name);
        let doc = format!(
            "The `{name}` topic's name, which `#[ocel::consumer(topic = {ident}::{constant})]` consumes."
        );
        Some(quote! {
            #[doc = #doc]
            #visibility const #constant: #krate::TopicName<#payload> = #krate::TopicName::new(#name);
        })
    });

    Ok(quote! {
        impl #krate::Declare for #ident {
            fn declared() -> #krate::Declared {
                #krate::Declared {
                    resources: ::std::vec![#(#declared),*],
                    variables: ::std::vec::Vec::new(),
                    groups: ::std::vec::Vec::new(),
                }
            }

            fn load() -> ::core::result::Result<Self, #krate::Error> {
                ::core::result::Result::Ok(Self { #(#fields),* })
            }
        }

        impl #ident {
            /// The struct with a handle in every field, ready to read bindings through.
            pub fn load() -> ::core::result::Result<Self, #krate::Error> {
                <Self as #krate::Declare>::load()
            }

            #(#topic_names)*
        }

        #krate::inventory::submit! {
            #krate::Registered(<#ident as #krate::Declare>::declared)
        }

        #(#handlers)*
    })
}

fn quote_worker_handler(
    krate: &syn::Path,
    ident: &syn::Ident,
    name: &str,
    concurrency: i32,
    on_start: Option<&syn::Path>,
    middleware: Option<&syn::Path>,
) -> TokenStream {
    let mut hooks = Vec::new();
    let on_start = match on_start {
        Some(path) => {
            let wrapper = format_ident!("__ocel_{}_on_start", ident);
            let call = quote_spanned!(path.span()=> ::std::boxed::Box::pin(#path()));
            hooks.push(quote! {
                fn #wrapper() -> #krate::BoxFuture<'static, ::core::result::Result<(), #krate::RunError>> {
                    #call
                }
            });
            quote!(::core::option::Option::Some(#wrapper))
        }
        None => quote_none(),
    };
    let middleware = match middleware {
        Some(path) => {
            let wrapper = format_ident!("__ocel_{}_middleware", ident);
            let call = quote_spanned!(path.span()=> ::std::boxed::Box::pin(#path(run, next)));
            hooks.push(quote! {
                fn #wrapper<'a>(
                    run: &'a #krate::Run,
                    next: #krate::Next<'a>,
                ) -> #krate::BoxFuture<'a, ::core::result::Result<(), #krate::RunError>> {
                    #call
                }
            });
            quote!(::core::option::Option::Some(#wrapper))
        }
        None => quote_none(),
    };
    quote! {
        const _: () = {
            #(#hooks)*

            #krate::inventory::submit! {
                #krate::Handler::Worker {
                    name: #name,
                    concurrency: #concurrency,
                    on_start: #on_start,
                    middleware: #middleware,
                }
            }
        };
    }
}

fn read_resources(input: &DeriveInput, krate: &syn::Path) -> syn::Result<Vec<Resource>> {
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "ocel::Resources wants a struct whose fields are the resources it declares.",
        ));
    };
    let Fields::Named(named) = &data.fields else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "an ocel::Resources struct has named fields.",
        ));
    };

    let mut resources: Vec<Resource> = Vec::new();
    for field in &named.named {
        let ident = field.ident.clone().expect("a named field");
        let Some(kind) = find_kind(&field.ty) else {
            return Err(syn::Error::new_spanned(
                &field.ty,
                format!(
                    "field {ident} is not an ocel::Postgres, an ocel::Bucket, an ocel::Topic<T>, an ocel::Worker or an ocel::Kv, and every field of an ocel::Resources struct declares one."
                ),
            ));
        };

        let mut name = ident.to_string();
        let mut config = match &kind {
            Kind::Postgres => Config::Postgres {
                version: DEFAULT_VERSION.to_string(),
            },
            Kind::Bucket => Config::Bucket {
                public: false,
                allowed_origins: Vec::new(),
            },
            Kind::Topic(payload) => Config::Topic {
                payload: (**payload).clone(),
                schema: quote_no_schema(),
                ordered: false,
                retry: quote_unset(),
            },
            Kind::Worker => Config::Worker {
                concurrency: 0,
                on_start: None,
                middleware: None,
            },
            Kind::Kv => Config::Kv {
                version: String::new(),
                eviction: String::new(),
                memory: String::new(),
                entries: Vec::new(),
            },
        };
        for entry in parse_entries(&field.attrs)? {
            read_entry(&ident, &kind, &mut name, &mut config, &entry, krate).map_err(|err| {
                match err.to_string().starts_with("field ") {
                    true => err,
                    false => syn::Error::new(err.span(), format!("field {ident}: {err}")),
                }
            })?;
        }
        if let Some(seen) = resources.iter().find(|seen| {
            seen.name == name && read_namespace(&seen.config) == read_namespace(&config)
        }) {
            return Err(syn::Error::new_spanned(
                &field.ident,
                format!(
                    "'{name}' is declared by both {} and {ident}. A name is declared by exactly one field.",
                    seen.ident
                ),
            ));
        }
        let (file, line) = source(ident.span());
        resources.push(Resource {
            ident,
            visibility: field.vis.clone(),
            name,
            config,
            file,
            line,
        });
    }
    Ok(resources)
}

fn read_entry(
    ident: &syn::Ident,
    kind: &Kind,
    name: &mut String,
    config: &mut Config,
    entry: &Entry,
    krate: &syn::Path,
) -> syn::Result<()> {
    match (config, entry.name.to_string().as_str()) {
        (_, "name") => *name = entry.read_literal()?.to_string(),
        (Config::Postgres { version }, "version") => *version = entry.read_literal()?.to_string(),
        (Config::Bucket { public, .. }, "public") => *public = true,
        (
            Config::Bucket {
                allowed_origins, ..
            },
            "allowed_origins",
        ) => *allowed_origins = entry.read_list()?.to_vec(),
        (Config::Topic { ordered, .. }, "ordered") => {
            entry.expect_flag()?;
            *ordered = true;
        }
        (Config::Topic { retry: policy, .. }, "retry") => *policy = parse_retry(entry, krate)?,
        (
            Config::Topic {
                schema, payload, ..
            },
            "schema",
        ) => *schema = quote_schema(entry, payload, krate)?,
        (Config::Worker { concurrency, .. }, "concurrency") => *concurrency = parse_count(entry)?,
        (Config::Worker { on_start, .. }, "on_start") => {
            *on_start = Some(entry.read_path()?.clone())
        }
        (Config::Worker { middleware, .. }, "middleware") => {
            *middleware = Some(entry.read_path()?.clone())
        }
        (Config::Kv { version, .. }, "version") => *version = read_kv_version(entry)?,
        (Config::Kv { eviction, .. }, "eviction") => *eviction = read_kv_eviction(entry)?,
        (Config::Kv { memory, .. }, "memory") => *memory = entry.read_literal()?.to_string(),
        (Config::Kv { entries, .. }, "entries") => *entries = entry.read_paths()?,
        (_, other) => {
            return Err(syn::Error::new(
                entry.span(),
                format!(
                "field {ident} has an unknown attribute '{other}'. The attributes of {} are {}.",
                kind.spelling(),
                kind.attributes(),
            ),
            ))
        }
    }
    Ok(())
}

fn read_namespace(config: &Config) -> &'static str {
    match config {
        Config::Postgres { .. } | Config::Bucket { .. } | Config::Kv { .. } => "resource",
        Config::Topic { .. } => "topic",
        Config::Worker { .. } => "worker",
    }
}

enum Kind {
    Postgres,
    Bucket,
    Topic(Box<Type>),
    Worker,
    Kv,
}

impl Kind {
    fn spelling(&self) -> &'static str {
        match self {
            Self::Postgres => "an ocel::Postgres",
            Self::Bucket => "an ocel::Bucket",
            Self::Topic(_) => "an ocel::Topic",
            Self::Worker => "an ocel::Worker",
            Self::Kv => "an ocel::Kv",
        }
    }

    fn attributes(&self) -> &'static str {
        match self {
            Self::Postgres => "name = \"<NAME>\" and version = \"<VERSION>\"",
            Self::Bucket => "name = \"<NAME>\", public and allowed_origins = [\"<ORIGIN>\"]",
            Self::Topic(_) => "name = \"<NAME>\", ordered, retry(...) and schema",
            Self::Worker => {
                "name = \"<NAME>\", concurrency = <NUMBER>, on_start = <PATH> and middleware = <PATH>"
            }
            Self::Kv => {
                "name = \"<NAME>\", version = \"<VERSION>\", eviction = \"<POLICY>\", memory = \"<SIZE>\" and entries = [<KEY TYPE>, ...]"
            }
        }
    }
}

fn find_kind(field_type: &Type) -> Option<Kind> {
    let Type::Path(path) = field_type else {
        return None;
    };
    if path.qself.is_some() {
        return None;
    }
    let last = path.path.segments.last()?;
    match last.ident.to_string().as_str() {
        "Postgres" => Some(Kind::Postgres),
        "Bucket" => Some(Kind::Bucket),
        "Worker" => Some(Kind::Worker),
        "Kv" => Some(Kind::Kv),
        "Topic" => {
            let PathArguments::AngleBracketed(arguments) = &last.arguments else {
                return None;
            };
            match arguments.args.first()? {
                GenericArgument::Type(payload) => Some(Kind::Topic(Box::new(payload.clone()))),
                _ => None,
            }
        }
        _ => None,
    }
}

const KV_VERSIONS: &[&str] = &["8", "9"];

const KV_EVICTIONS: &[&str] = &[
    "noeviction",
    "allkeys-lru",
    "allkeys-lfu",
    "allkeys-random",
    "volatile-lru",
    "volatile-lfu",
    "volatile-random",
    "volatile-ttl",
];

fn read_kv_version(entry: &Entry) -> syn::Result<String> {
    let version = entry.read_literal()?;
    if !KV_VERSIONS.contains(&version) {
        return Err(syn::Error::new(
            entry.span(),
            format!(
                "'version' is \"{version}\", and a store runs version {}.",
                KV_VERSIONS.join(" or ")
            ),
        ));
    }
    Ok(version.to_string())
}

fn read_kv_eviction(entry: &Entry) -> syn::Result<String> {
    let eviction = entry.read_literal()?;
    if !KV_EVICTIONS.contains(&eviction) {
        return Err(syn::Error::new(
            entry.span(),
            format!(
                "'eviction' is \"{eviction}\", and a store evicts by one of {}.",
                KV_EVICTIONS.join(", ")
            ),
        ));
    }
    Ok(eviction.to_string())
}

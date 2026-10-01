use crate::attribute::{parse_arguments, Entry, Value};
use crate::options::{
    parse_batch, parse_batch_size, parse_count, parse_duration, parse_lanes, parse_retry,
    parse_string, quote_batch, quote_none, refuse_unknown_attribute,
};
use crate::source::source;
use proc_macro2::{Span, TokenStream};
use quote::{format_ident, quote, quote_spanned};
use syn::spanned::Spanned;
use syn::{FnArg, GenericArgument, ItemFn, PathArguments, ReturnType, Type};

const TASK_ATTRIBUTES: &str = "name = \"<NAME>\", retry(...), concurrency = <NUMBER>, max_duration = \"<DURATION>\", ttl = \"<DURATION>\", ordered, batch(...), worker = \"<WORKER>\", cron = \"<CRON>\", schema, on_success, on_failure, on_complete, on_cancel, catch_error, middleware and on_start_attempt";
const CONSUMER_ATTRIBUTES: &str = "topic = <TOPIC NAME>, name = \"<NAME>\", retry(...), concurrency = <NUMBER>, lanes = [\"<LANE>\"], max_duration = \"<DURATION>\" and worker = \"<WORKER>\"";
const BATCH_CONSUMER_ATTRIBUTES: &str = "topic = <TOPIC NAME>, batch_size = <NUMBER>, batch_timeout = \"<DURATION>\", name = \"<NAME>\", retry(...), concurrency = <NUMBER>, lanes = [\"<LANE>\"], max_duration = \"<DURATION>\" and worker = \"<WORKER>\"";
const PAYLOAD_HOOKS: [&str; 7] = [
    "on_start_attempt",
    "middleware",
    "catch_error",
    "on_success",
    "on_failure",
    "on_complete",
    "on_cancel",
];

struct Signature {
    payload: Type,
    output: Type,
}

fn read_signature(function: &ItemFn, macro_name: &str, batched: bool) -> syn::Result<Signature> {
    let wanted = format!(
        "a #[ocel::{macro_name}] function is `async fn {}(payload: {}, run: &ocel::Run) -> Result<{}, ocel::RunError>`",
        function.sig.ident,
        if batched { "Vec<P>" } else { "P" },
        if macro_name == "task" { "R" } else { "()" },
    );
    let refuse = |span: Span| syn::Error::new(span, format!("{wanted}."));
    if function.sig.asyncness.is_none() {
        return Err(refuse(function.sig.fn_token.span));
    }
    if !function.sig.generics.params.is_empty() {
        return Err(syn::Error::new_spanned(
            &function.sig.generics,
            format!("{wanted}, with no generic parameters, because it registers once per binary."),
        ));
    }
    let inputs: Vec<&FnArg> = function.sig.inputs.iter().collect();
    let [FnArg::Typed(payload), FnArg::Typed(_)] = inputs.as_slice() else {
        return Err(refuse(function.sig.inputs.span()));
    };
    let payload = (*payload.ty).clone();
    let ReturnType::Type(_, returned) = &function.sig.output else {
        return Err(refuse(function.sig.ident.span()));
    };
    let Some(output) = find_ok_type(returned) else {
        return Err(refuse(returned.span()));
    };
    Ok(Signature { payload, output })
}

fn find_ok_type(returned: &Type) -> Option<Type> {
    find_first_argument(returned, "Result")
}

fn find_batch_item(payload: &Type) -> Option<Type> {
    find_first_argument(payload, "Vec")
}

fn find_first_argument(generic: &Type, name: &str) -> Option<Type> {
    let Type::Path(path) = generic else {
        return None;
    };
    let last = path.path.segments.last()?;
    if last.ident != name {
        return None;
    }
    let PathArguments::AngleBracketed(arguments) = &last.arguments else {
        return None;
    };
    match arguments.args.first()? {
        GenericArgument::Type(argument) => Some(argument.clone()),
        _ => None,
    }
}

fn read_batch_item(payload: &Type, macro_name: &str) -> syn::Result<Type> {
    find_batch_item(payload).ok_or_else(|| {
        syn::Error::new(
            payload.span(),
            format!("a #[ocel::{macro_name}] function takes its payloads as a Vec<P>."),
        )
    })
}

fn derive_default_name(function: &ItemFn) -> String {
    function
        .sig
        .ident
        .to_string()
        .trim_start_matches("r#")
        .replace('_', "-")
}

fn quote_schema(entry: &Entry, payload: &Type) -> syn::Result<TokenStream> {
    entry.expect_flag()?;
    Ok(quote_spanned!(entry.span()=> ::ocel::__json_schema!(#payload)))
}

struct Hook {
    field: &'static str,
    wrapper: TokenStream,
}

fn parse_hook(entry: &Entry, steps_payload: &Type, output: &Type) -> syn::Result<Option<Hook>> {
    let name = entry.name.to_string();
    let Some(field) = PAYLOAD_HOOKS.into_iter().find(|hook| *hook == name) else {
        return Ok(None);
    };
    let wrapper = format_ident!("__ocel_{}", field);
    let run = quote!(::ocel::Run);
    let failure = quote!(::core::result::Result<(), ::ocel::RunError>);
    let (signature, call) = match field {
        "on_success" => (
            quote!((payload: &'a #steps_payload, output: &'a #output, run: &'a #run) -> ::ocel::BoxFuture<'a, #failure>),
            quote!((payload, output, run)),
        ),
        "on_failure" => (
            quote!((payload: &'a #steps_payload, error: &'a ::ocel::RunError, run: &'a #run) -> ::ocel::BoxFuture<'a, #failure>),
            quote!((payload, error, run)),
        ),
        "on_complete" => (
            quote!((payload: &'a #steps_payload, result: ::core::result::Result<&'a #output, &'a ::ocel::RunError>, run: &'a #run) -> ::ocel::BoxFuture<'a, #failure>),
            quote!((payload, result, run)),
        ),
        "catch_error" => (
            quote!((payload: &'a #steps_payload, error: ::ocel::RunError, run: &'a #run) -> ::ocel::BoxFuture<'a, ::ocel::RunError>),
            quote!((payload, error, run)),
        ),
        "middleware" => (
            quote!((payload: &'a #steps_payload, run: &'a #run, next: ::ocel::Next<'a, #output>) -> ::ocel::BoxFuture<'a, ::core::result::Result<#output, ::ocel::RunError>>),
            quote!((payload, run, next)),
        ),
        _ => (
            quote!((payload: &'a #steps_payload, run: &'a #run) -> ::ocel::BoxFuture<'a, #failure>),
            quote!((payload, run)),
        ),
    };
    let path = entry.read_path()?;
    let body = quote_spanned!(path.span()=> ::std::boxed::Box::pin(#path #call));
    Ok(Some(Hook {
        field,
        wrapper: quote! {
            fn #wrapper<'a> #signature {
                #body
            }
        },
    }))
}

pub(crate) fn expand_task(attribute: TokenStream, function: ItemFn) -> syn::Result<TokenStream> {
    let entries = parse_arguments(attribute)?;
    let batched = entries.iter().any(|entry| entry.name == "batch");
    let Signature { payload, output } = read_signature(&function, "task", batched)?;
    let trigger_payload = match batched {
        true => find_batch_item(&payload).ok_or_else(|| {
            syn::Error::new(
                payload.span(),
                "a task with batch(...) takes its payloads as a Vec<P>, and triggers each P.",
            )
        })?,
        false => payload.clone(),
    };

    let mut name = derive_default_name(&function);
    let mut schema = quote_none();
    let mut ordered = false;
    let mut retry = quote_none();
    let mut concurrency = 0;
    let mut max_duration = quote_none();
    let mut ttl = quote_none();
    let mut batch = quote_none();
    let mut worker = String::new();
    let mut cron = String::new();
    let mut hooks: Vec<Hook> = Vec::new();
    for entry in &entries {
        match entry.name.to_string().as_str() {
            "name" => name = parse_string(entry)?,
            "schema" => schema = quote_schema(entry, &trigger_payload)?,
            "ordered" => {
                entry.expect_flag()?;
                ordered = true;
            }
            "retry" => retry = parse_retry(entry)?,
            "concurrency" => concurrency = parse_count(entry)?,
            "max_duration" => max_duration = parse_duration(entry)?,
            "ttl" => ttl = parse_duration(entry)?,
            "batch" => batch = parse_batch(entry)?,
            "worker" => worker = parse_string(entry)?,
            "cron" => cron = parse_string(entry)?,
            other => match parse_hook(entry, &payload, &output)? {
                Some(hook) => hooks.push(hook),
                None => {
                    return Err(refuse_unknown_attribute(
                        entry,
                        other,
                        "#[ocel::task]",
                        TASK_ATTRIBUTES,
                    ))
                }
            },
        }
    }

    let ident = &function.sig.ident;
    let visibility = &function.vis;
    let docs = function
        .attrs
        .iter()
        .filter(|attribute| attribute.path().is_ident("doc"));
    let mut inner = function.clone();
    inner
        .attrs
        .retain(|attribute| !attribute.path().is_ident("doc"));
    inner.vis = syn::Visibility::Inherited;
    inner.sig.ident = format_ident!("__ocel_run");
    let (file, line) = source(ident.span());

    let wrappers = hooks.iter().map(|hook| &hook.wrapper);
    let fields = PAYLOAD_HOOKS.map(|field| {
        let field_ident = format_ident!("{}", field);
        match hooks.iter().any(|hook| hook.field == field) {
            true => {
                let wrapper = format_ident!("__ocel_{}", field);
                quote!(#field_ident: ::core::option::Option::Some(#wrapper))
            }
            false => quote!(#field_ident: ::core::option::Option::None),
        }
    });
    let clone_payload = match hooks.is_empty() {
        true => quote_none(),
        false => quote_spanned! {payload.span()=>
            ::core::option::Option::Some(<#payload as ::core::clone::Clone>::clone)
        },
    };

    Ok(quote! {
        #(#docs)*
        #[allow(non_upper_case_globals)]
        #visibility static #ident: ::ocel::Task<#trigger_payload, #output> = ::ocel::Task::new(#name);

        const _: () = {
            #inner

            fn __ocel_boxed<'a>(
                payload: #payload,
                run: &'a ::ocel::Run,
            ) -> ::ocel::BoxFuture<'a, ::core::result::Result<#output, ::ocel::RunError>> {
                ::std::boxed::Box::pin(__ocel_run(payload, run))
            }

            #(#wrappers)*

            static __OCEL_STEPS: ::ocel::Steps<#payload, #output> = ::ocel::Steps {
                run: __ocel_boxed,
                clone_payload: #clone_payload,
                #(#fields),*
            };

            fn __ocel_deliver(
                delivery: ::ocel::Delivery,
            ) -> ::ocel::BoxFuture<'static, ::ocel::Outcome> {
                ::ocel::run_attempt(&__OCEL_STEPS, delivery)
            }

            fn __ocel_declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec![::ocel::DeclaredResource {
                        name: #name,
                        config: ::ocel::DeclaredConfig::Task {
                            schema: #schema,
                            ordered: #ordered,
                            retry: #retry,
                            concurrency: #concurrency,
                            max_duration: #max_duration,
                            ttl: #ttl,
                            batch: #batch,
                            worker: #worker,
                            cron: #cron,
                            deliver: __ocel_deliver,
                        },
                        file: #file,
                        line: #line,
                    }],
                    variables: ::std::vec::Vec::new(),
                    groups: ::std::vec::Vec::new(),
                }
            }

            ::ocel::inventory::submit! {
                ::ocel::Registered(__ocel_declared)
            }
        };
    })
}

pub(crate) fn expand_consumer(
    attribute: TokenStream,
    function: ItemFn,
) -> syn::Result<TokenStream> {
    expand_topic_consumer(attribute, function, false)
}

pub(crate) fn expand_batch_consumer(
    attribute: TokenStream,
    function: ItemFn,
) -> syn::Result<TokenStream> {
    expand_topic_consumer(attribute, function, true)
}

fn expand_topic_consumer(
    attribute: TokenStream,
    function: ItemFn,
    batched: bool,
) -> syn::Result<TokenStream> {
    let (macro_name, known) = match batched {
        true => ("batch_consumer", BATCH_CONSUMER_ATTRIBUTES),
        false => ("consumer", CONSUMER_ATTRIBUTES),
    };
    let owner = format!("#[ocel::{macro_name}]");
    let entries = parse_arguments(attribute)?;
    let Signature { payload, output } = read_signature(&function, macro_name, batched)?;
    let message = match batched {
        true => read_batch_item(&payload, macro_name)?,
        false => payload.clone(),
    };

    let mut topic: Option<syn::Path> = None;
    let mut name = derive_default_name(&function);
    let mut retry = quote_none();
    let mut concurrency = 0;
    let mut lanes = quote!(&[]);
    let mut max_duration = quote_none();
    let mut worker = String::new();
    let mut batch_size: Option<i32> = None;
    let mut batch_timeout = quote_none();
    for entry in &entries {
        match (entry.name.to_string().as_str(), batched) {
            ("topic", _) => topic = Some(read_topic_name(entry, &owner)?),
            ("name", _) => name = parse_string(entry)?,
            ("retry", _) => retry = parse_retry(entry)?,
            ("concurrency", _) => concurrency = parse_count(entry)?,
            ("lanes", _) => lanes = parse_lanes(entry)?,
            ("max_duration", _) => max_duration = parse_duration(entry)?,
            ("worker", _) => worker = parse_string(entry)?,
            ("batch_size", true) => batch_size = Some(parse_batch_size(entry)?),
            ("batch_timeout", true) => batch_timeout = parse_duration(entry)?,
            (other, _) => return Err(refuse_unknown_attribute(entry, other, &owner, known)),
        }
    }
    let Some(topic) = topic else {
        return Err(syn::Error::new(
            Span::call_site(),
            format!("{owner} names the topic it consumes by the constant its struct holds for it: #[ocel::{macro_name}(topic = Infra::ORDERS)]."),
        ));
    };
    let batch = match (batched, batch_size) {
        (false, _) => quote_none(),
        (true, Some(size)) => quote_batch(size, batch_timeout),
        (true, None) => {
            return Err(syn::Error::new(
                Span::call_site(),
                format!("{owner} names how many messages a batch holds: batch_size = <NUMBER>."),
            ))
        }
    };

    let ident = &function.sig.ident;
    let (file, line) = source(ident.span());
    let topic_name = quote_spanned! {topic.span()=>
        const __OCEL_TOPIC: ::ocel::TopicName<#message> = #topic;
    };
    Ok(quote! {
        #function

        const _: () = {
            #topic_name

            fn __ocel_boxed<'a>(
                payload: #payload,
                run: &'a ::ocel::Run,
            ) -> ::ocel::BoxFuture<'a, ::core::result::Result<#output, ::ocel::RunError>> {
                ::std::boxed::Box::pin(#ident(payload, run))
            }

            static __OCEL_STEPS: ::ocel::Steps<#payload, ()> = ::ocel::Steps::new(__ocel_boxed);

            fn __ocel_deliver(
                delivery: ::ocel::Delivery,
            ) -> ::ocel::BoxFuture<'static, ::ocel::Outcome> {
                ::ocel::run_attempt(&__OCEL_STEPS, delivery)
            }

            fn __ocel_declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec![::ocel::DeclaredResource {
                        name: #name,
                        config: ::ocel::DeclaredConfig::Consumer {
                            topic: __OCEL_TOPIC.name(),
                            worker: #worker,
                            retry: #retry,
                            concurrency: #concurrency,
                            max_duration: #max_duration,
                            lanes: #lanes,
                            batch: #batch,
                            deliver: __ocel_deliver,
                        },
                        file: #file,
                        line: #line,
                    }],
                    variables: ::std::vec::Vec::new(),
                    groups: ::std::vec::Vec::new(),
                }
            }

            ::ocel::inventory::submit! {
                ::ocel::Registered(__ocel_declared)
            }
        };
    })
}

fn read_topic_name(entry: &Entry, owner: &str) -> syn::Result<syn::Path> {
    match &entry.value {
        Value::Path(path) => Ok(path.clone()),
        _ => Err(syn::Error::new(
            entry.span(),
            format!("{owner} names its topic by the constant its struct holds for it, such as topic = Infra::ORDERS for the field `orders` of `Infra`, not by a string."),
        )),
    }
}

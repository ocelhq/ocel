use crate::attribute::parse_entries;
use crate::options::{quote_none, read_duration, refuse_unknown_attribute};
use crate::source::source;
use proc_macro2::{Span, TokenStream};
use quote::{quote, quote_spanned};
use syn::{Data, DeriveInput, Fields};

const MAX_SEGMENTS: usize = 4;
const SEGMENT_RULE: &str =
    "letters, digits and -, at most 50 characters, starting and ending with a letter or digit";
const ATTRIBUTES: &str = "realtime = \"<NAME>\", pattern = \"<PATTERN>\", event = <TYPE>, wildcard, public, publish, schema and token_ttl = \"<DURATION>\"";
const MIN_TOKEN_TTL_SECONDS: u64 = 10;
const MAX_TOKEN_TTL_SECONDS: u64 = 300;

fn is_channel_segment(text: &str) -> bool {
    let bytes = text.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 50
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'-')
        && bytes[0].is_ascii_alphanumeric()
        && bytes[bytes.len() - 1].is_ascii_alphanumeric()
}

fn parse_parameters(written: &str) -> Result<Vec<String>, String> {
    if written.is_empty() {
        return Err(format!("the pattern is empty: write one to {MAX_SEGMENTS} segments joined by /, each a literal or a :parameter"));
    }
    let parts: Vec<&str> = written.split('/').collect();
    if parts.len() > MAX_SEGMENTS {
        return Err(format!(
            "pattern \"{written}\" has {} segments, and a channel pattern has at most {MAX_SEGMENTS}",
            parts.len()
        ));
    }
    let mut parameters: Vec<String> = Vec::new();
    for part in parts {
        if part.is_empty() {
            return Err(format!("pattern \"{written}\" has an empty segment: it neither starts nor ends with /, and no two / are adjacent"));
        }
        if let Some(name) = part.strip_prefix(':') {
            let mut characters = name.chars();
            let starts = characters
                .next()
                .is_some_and(|first| first.is_ascii_alphabetic() || first == '_');
            if !starts || !characters.all(|c| c.is_ascii_alphanumeric() || c == '_') {
                return Err(format!("pattern \"{written}\" has segment \"{part}\", which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _"));
            }
            if parameters.iter().any(|seen| seen == name) {
                return Err(format!("pattern \"{written}\" names parameter \"{name}\" twice, so a channel could not say which value is which"));
            }
            parameters.push(name.to_string());
        } else if !is_channel_segment(part) {
            return Err(format!("pattern \"{written}\" has segment \"{part}\", which is no literal: a literal is {SEGMENT_RULE}"));
        }
    }
    Ok(parameters)
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let ident = &input.ident;
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            ident,
            "ocel::Channel wants a struct whose fields are the params of its pattern.",
        ));
    };
    let fields: Vec<&syn::Ident> = match &data.fields {
        Fields::Named(named) => named
            .named
            .iter()
            .map(|field| field.ident.as_ref().expect("a named field"))
            .collect(),
        Fields::Unit => Vec::new(),
        Fields::Unnamed(_) => {
            return Err(syn::Error::new_spanned(
                ident,
                "an ocel::Channel struct has named fields, one String for each param of its pattern, or none for a pattern without params.",
            ))
        }
    };

    let mut realtime: Option<(String, Span)> = None;
    let mut pattern: Option<(String, Span)> = None;
    let mut event: Option<syn::Path> = None;
    let mut schema = quote_none();
    let mut wildcard = false;
    let mut public = false;
    let mut publish = false;
    let mut schema_entry = None;
    let mut token_ttl = quote_none();
    for entry in parse_entries(&input.attrs)? {
        match entry.name.to_string().as_str() {
            "realtime" => realtime = Some((entry.read_literal()?.to_string(), entry.span())),
            "pattern" => pattern = Some((entry.read_literal()?.to_string(), entry.span())),
            "event" => event = Some(entry.read_path()?.clone()),
            "wildcard" => {
                entry.expect_flag()?;
                wildcard = true;
            }
            "public" => {
                entry.expect_flag()?;
                public = true;
            }
            "publish" => {
                entry.expect_flag()?;
                publish = true;
            }
            "schema" => {
                entry.expect_flag()?;
                schema_entry = Some(entry.span());
            }
            "token_ttl" => {
                let (seconds, nanos) = read_duration(&entry)?;
                if nanos != 0 || !(MIN_TOKEN_TTL_SECONDS..=MAX_TOKEN_TTL_SECONDS).contains(&seconds)
                {
                    return Err(syn::Error::new(
                        entry.span(),
                        format!("token_ttl is \"{}\", and a token lives {MIN_TOKEN_TTL_SECONDS}s to {MAX_TOKEN_TTL_SECONDS}s in whole seconds: long enough to open a socket and no longer.", entry.read_literal()?),
                    ));
                }
                token_ttl = quote!(::core::option::Option::Some(::core::time::Duration::from_secs(#seconds)));
            }
            other => {
                return Err(refuse_unknown_attribute(
                    &entry,
                    other,
                    &format!("the ocel::Channel {ident}"),
                    ATTRIBUTES,
                ))
            }
        }
    }

    let Some((realtime, realtime_span)) = realtime else {
        return Err(syn::Error::new_spanned(
            ident,
            format!("{ident} names no realtime resource: #[ocel(realtime = \"<NAME>\", ...)]."),
        ));
    };
    if !is_channel_segment(&realtime) {
        return Err(syn::Error::new(
            realtime_span,
            format!("realtime \"{realtime}\" names no channel namespace: the name begins every channel, so it is {SEGMENT_RULE}."),
        ));
    }
    let Some((pattern, pattern_span)) = pattern else {
        return Err(syn::Error::new_spanned(
            ident,
            format!("{ident} declares no pattern: #[ocel(pattern = \"<PATTERN>\", ...)], such as \"orders/:order_id\"."),
        ));
    };
    let Some(event) = event else {
        return Err(syn::Error::new_spanned(
            ident,
            format!("{ident} declares no event: #[ocel(event = <TYPE>, ...)], the type every channel of the pattern carries."),
        ));
    };
    if let Some(span) = schema_entry {
        schema = quote_spanned!(span=> ::ocel::__json_schema!(#event));
    }
    let parameters =
        parse_parameters(&pattern).map_err(|reason| syn::Error::new(pattern_span, reason))?;
    if wildcard && parameters.is_empty() {
        return Err(syn::Error::new(
            pattern_span,
            format!("pattern \"{pattern}\" sets wildcard, and a pattern with no params has no trailing param a subscriber could leave off."),
        ));
    }
    for field in &fields {
        if !parameters.contains(&field.to_string()) {
            return Err(syn::Error::new_spanned(
                field,
                format!("field {field} names no param of pattern \"{pattern}\": the fields of {ident} are its params."),
            ));
        }
    }
    for parameter in &parameters {
        if !fields.iter().any(|field| *field == parameter.as_str()) {
            return Err(syn::Error::new(
                pattern_span,
                format!("pattern \"{pattern}\" has param :{parameter}, and {ident} has no field {parameter}: add one."),
            ));
        }
    }

    let names: Vec<String> = fields.iter().map(ToString::to_string).collect();
    let construct = if matches!(data.fields, Fields::Unit) {
        quote!(Self)
    } else {
        quote!(Self { #(#fields: params.get(#names).cloned().unwrap_or_default()),* })
    };
    let (file, line) = source(ident.span());
    Ok(quote! {
        ::ocel::__realtime_channel! {
        impl ::ocel::realtime::Channel for #ident {
            type Event = #event;
            const REALTIME: &'static str = #realtime;
            const PATTERN: &'static str = #pattern;

            fn to_params(&self) -> ::std::collections::BTreeMap<::std::string::String, ::std::string::String> {
                let mut params = ::std::collections::BTreeMap::new();
                #(params.insert(::std::string::String::from(#names), ::std::string::String::clone(&self.#fields));)*
                params
            }

            fn from_params(params: &::std::collections::BTreeMap<::std::string::String, ::std::string::String>) -> Self {
                let _ = params;
                #construct
            }
        }

        ::ocel::inventory::submit! {
            ::ocel::realtime::RegisteredChannel(|| ::ocel::realtime::DeclaredChannel {
                realtime: #realtime,
                pattern: #pattern,
                wildcard: #wildcard,
                public: #public,
                publish: #publish,
                schema: #schema,
                token_ttl: #token_ttl,
                file: #file,
                line: #line,
            })
        }
        }
    })
}

#[cfg(test)]
mod tests {
    use super::parse_parameters;

    #[test]
    fn a_pattern_of_literal_and_parameter_segments_names_its_parameters_in_order() {
        assert_eq!(
            parse_parameters("projects/:project_id/deploys/:deploy_id"),
            Ok(vec!["project_id".to_string(), "deploy_id".to_string()])
        );
        assert_eq!(parse_parameters("status"), Ok(Vec::new()));
    }

    #[test]
    fn a_channel_pattern_outside_the_grammar_is_refused_saying_why() {
        for (written, reason) in [
            ("", "empty"),
            ("a/b/c/d/e", "at most 4"),
            ("orders//x", "empty segment"),
            ("orders/:1x", "no parameter"),
            ("orders_x", "no literal"),
            ("rooms/:id/:id", "twice"),
        ] {
            let refused = parse_parameters(written).err().unwrap_or_default();
            assert!(refused.contains(reason), "{written:?}: {refused}");
        }
    }
}

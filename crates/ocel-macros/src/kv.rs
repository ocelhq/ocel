use crate::attribute::{parse_entries, Value};
use crate::options::{parse_duration, quote_none, refuse_unknown_attribute};
use crate::source::source;
use proc_macro2::{Span, TokenStream};
use quote::{quote, quote_spanned};
use std::collections::BTreeSet;
use syn::{Data, DeriveInput, Fields};

const MAX_ENTRY_NAME_BYTES: usize = 63;
const RESERVED_ENTRY_NAMES: &[&str] = &[
    "client",
    "connectionString",
    "connection_string",
    "then",
    "constructor",
];
const ATTRIBUTES: &str = "pattern = \"<PATTERN>\", one of text, counter, json = <TYPE>, list and set, ttl = \"<DURATION>\", on_invalid = \"miss\" and name = \"<NAME>\"";

enum Segment {
    Literal(String),
    Parameter(String),
}

fn parse_pattern(written: &str) -> Result<Vec<Segment>, String> {
    if written.is_empty() {
        return Err("the pattern is empty: write one or more segments joined by /, each a literal or a :parameter".into());
    }
    if written.contains(['{', '}']) {
        return Err(format!(
            "pattern \"{written}\" holds {{ or }}, which a store reserves as the hash tag syntax"
        ));
    }
    let mut segments = Vec::new();
    let mut named = BTreeSet::new();
    for part in written.split('/') {
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
            if !named.insert(name.to_string()) {
                return Err(format!("pattern \"{written}\" names parameter \"{name}\" twice, so a key could not say which value is which"));
            }
            segments.push(Segment::Parameter(name.to_string()));
        } else if part
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-'))
        {
            segments.push(Segment::Literal(part.to_string()));
        } else {
            return Err(format!("pattern \"{written}\" has segment \"{part}\", which is no literal: a literal is letters, digits, ., _ and -"));
        }
    }
    Ok(segments)
}

fn refuse_entry_name(name: &str) -> Result<(), String> {
    if RESERVED_ENTRY_NAMES.contains(&name) {
        return Err(format!(
            "the entry name '{name}' is reserved, since a store already has a member of that name in some SDK: name the entry with name = \"<NAME>\""
        ));
    }
    let mut characters = name.chars();
    let starts = characters.next().is_some_and(|c| c.is_ascii_alphabetic());
    if !starts
        || name.len() > MAX_ENTRY_NAME_BYTES
        || !characters.all(|c| c.is_ascii_alphanumeric() || c == '_')
    {
        return Err(format!("the entry name '{name}' is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most {MAX_ENTRY_NAME_BYTES} characters"));
    }
    Ok(())
}

fn snake_case(ident: &str) -> String {
    let mut out = String::new();
    for (index, character) in ident.chars().enumerate() {
        if character.is_ascii_uppercase() {
            if index > 0 {
                out.push('_');
            }
            out.push(character.to_ascii_lowercase());
        } else {
            out.push(character);
        }
    }
    out
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let ident = &input.ident;
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            ident,
            "ocel::KvKey wants a struct whose fields are the parameters of its pattern.",
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
                "an ocel::KvKey struct has named fields, one for each parameter of its pattern, or none for a pattern without parameters.",
            ))
        }
    };

    let mut pattern: Option<(String, Span)> = None;
    let mut shape: Option<(TokenStream, TokenStream, Span)> = None;
    let mut ttl = quote_none();
    let mut miss_on_invalid = false;
    let mut name = snake_case(&ident.to_string());
    let mut name_span = ident.span();
    for entry in parse_entries(&input.attrs)? {
        let attribute = entry.name.to_string();
        let shaped = |wire: TokenStream, marker: TokenStream| -> syn::Result<_> {
            entry.expect_flag()?;
            Ok((wire, marker, entry.span()))
        };
        let declared_shape = match attribute.as_str() {
            "pattern" => {
                pattern = Some((entry.read_literal()?.to_string(), entry.span()));
                None
            }
            "ttl" => {
                ttl = parse_duration(&entry)?;
                None
            }
            "on_invalid" => {
                match entry.read_literal()? {
                    "miss" => miss_on_invalid = true,
                    "raise" => miss_on_invalid = false,
                    other => {
                        return Err(syn::Error::new(
                            entry.span(),
                            format!(
                                "'on_invalid' is \"{other}\", and it is \"miss\" or \"raise\"."
                            ),
                        ))
                    }
                }
                None
            }
            "name" => {
                name = entry.read_literal()?.to_string();
                name_span = entry.span();
                None
            }
            "text" => Some(shaped(quote!(Text), quote!(::ocel::kv::Text))?),
            "counter" => Some(shaped(quote!(Counter), quote!(::ocel::kv::Counter))?),
            "list" => Some(shaped(quote!(List), quote!(::ocel::kv::List))?),
            "set" => Some(shaped(quote!(Set), quote!(::ocel::kv::Set))?),
            "json" => {
                let Value::Path(value) = &entry.value else {
                    return Err(syn::Error::new(
                        entry.span(),
                        "'json' wants the type its values are: json = <TYPE>.",
                    ));
                };
                Some((
                    quote!(Json),
                    quote_spanned!(entry.span()=> ::ocel::kv::Json<#value>),
                    entry.span(),
                ))
            }
            other => {
                return Err(refuse_unknown_attribute(
                    &entry,
                    other,
                    &format!("the ocel::KvKey {ident}"),
                    ATTRIBUTES,
                ))
            }
        };
        if let Some(declared) = declared_shape {
            if shape.is_some() {
                return Err(syn::Error::new(
                    declared.2,
                    format!("{ident} declares two shapes, and an entry has one: text, counter, json = <TYPE>, list or set."),
                ));
            }
            shape = Some(declared);
        }
    }

    let Some((pattern, pattern_span)) = pattern else {
        return Err(syn::Error::new_spanned(
            ident,
            format!("{ident} declares no pattern: #[ocel(pattern = \"<PATTERN>\", ...)], such as \"session/:id\"."),
        ));
    };
    let Some((wire, marker, _)) = shape else {
        return Err(syn::Error::new_spanned(
            ident,
            format!("{ident} declares no shape: one of text, counter, json = <TYPE>, list or set."),
        ));
    };
    if miss_on_invalid && wire.to_string() != "Json" {
        return Err(syn::Error::new_spanned(
            ident,
            "on_invalid applies to a json entry alone, whose stored value can fail to decode.",
        ));
    }
    refuse_entry_name(&name).map_err(|reason| syn::Error::new(name_span, reason))?;
    let segments =
        parse_pattern(&pattern).map_err(|reason| syn::Error::new(pattern_span, reason))?;

    let parameters: Vec<&str> = segments
        .iter()
        .filter_map(|segment| match segment {
            Segment::Parameter(name) => Some(name.as_str()),
            Segment::Literal(_) => None,
        })
        .collect();
    for field in &fields {
        if !parameters.contains(&field.to_string().as_str()) {
            return Err(syn::Error::new_spanned(
                field,
                format!("field {field} names no parameter of pattern \"{pattern}\": the fields of {ident} are its parameters, {}.", describe_parameters(&parameters)),
            ));
        }
    }
    for parameter in &parameters {
        if !fields.iter().any(|field| field == parameter) {
            return Err(syn::Error::new(
                pattern_span,
                format!("pattern \"{pattern}\" has parameter :{parameter}, and {ident} has no field {parameter}: add one."),
            ));
        }
    }

    let writes = segments.iter().enumerate().map(|(index, segment)| {
        let separator = (index > 0).then(|| quote!(key.push('/');));
        match segment {
            Segment::Literal(literal) => quote!(#separator key.push_str(#literal);),
            Segment::Parameter(parameter) => {
                let field = fields
                    .iter()
                    .find(|field| **field == parameter.as_str())
                    .expect("a field for every parameter");
                quote!(#separator ::ocel::kv::write_parameter(&mut key, &self.#field);)
            }
        }
    });
    let (file, line) = source(ident.span());
    Ok(quote! {
        impl ::ocel::KvKey for #ident {
            type Shape = #marker;

            const ENTRY: ::ocel::kv::KvEntryDeclaration = ::ocel::kv::KvEntryDeclaration {
                name: #name,
                pattern: #pattern,
                shape: ::ocel::kv::ShapeKind::#wire,
                ttl: #ttl,
                miss_on_invalid: #miss_on_invalid,
                file: #file,
                line: #line,
            };

            fn build_key(&self) -> ::std::string::String {
                let mut key = ::std::string::String::new();
                #(#writes)*
                key
            }
        }
    })
}

fn describe_parameters(parameters: &[&str]) -> String {
    if parameters.is_empty() {
        return "of which it has none".into();
    }
    parameters
        .iter()
        .map(|parameter| format!(":{parameter}"))
        .collect::<Vec<_>>()
        .join(", ")
}

#[cfg(test)]
mod tests {
    use super::{parse_pattern, refuse_entry_name, snake_case};

    #[test]
    fn a_pattern_of_literal_and_parameter_segments_parses() {
        for written in [
            "config",
            "requests/:user_id",
            "rooms/:room/members/:member",
            "v1.cache/feature-flags/:flag_name",
        ] {
            assert!(parse_pattern(written).is_ok(), "{written} refused");
        }
    }

    #[test]
    fn a_malformed_pattern_is_refused_saying_what_is_wrong() {
        for (written, says) in [
            ("", "empty"),
            ("/session/:id", "empty segment"),
            ("session//:id", "empty segment"),
            ("session/{id}", "hash tag"),
            ("session/:", "parameter"),
            ("session/:1id", "parameter"),
            ("session/:id/:id", "twice"),
            ("session:id", "literal"),
        ] {
            let refused = parse_pattern(written).err().expect("a refusal");
            assert!(refused.contains(says), "{written}: {refused}");
        }
    }

    #[test]
    fn an_entry_name_is_one_every_sdk_can_hold() {
        assert!(refuse_entry_name("session_key").is_ok());
        assert!(refuse_entry_name("client").is_err());
        assert!(refuse_entry_name("connection_string").is_err());
        assert!(refuse_entry_name("_private").is_err());
    }

    #[test]
    fn an_entry_is_named_after_its_struct_in_snake_case() {
        assert_eq!(snake_case("SessionKey"), "session_key");
        assert_eq!(snake_case("Requests"), "requests");
    }
}

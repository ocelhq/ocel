use crate::attribute::entries;
use proc_macro2::Span;
use syn::{Field, GenericArgument, PathArguments, Type};

const RESERVED_PREFIX: &str = "OCEL_";
const URL_KEY: &str = "OCEL_URL";

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Class {
    Plain,
    Sensitive,
    Secret,
}

impl Class {
    fn name(self) -> &'static str {
        match self {
            Self::Plain => "plain",
            Self::Sensitive => "sensitive",
            Self::Secret => "secret",
        }
    }
}

pub(crate) enum Shape {
    Live,
    Optional(Type),
    Direct(Type),
}

pub(crate) struct Variable {
    pub(crate) ident: syn::Ident,
    pub(crate) key: String,
    pub(crate) class: Class,
    pub(crate) folders: Vec<String>,
    pub(crate) fallback: Option<String>,
    pub(crate) description: Option<String>,
    pub(crate) shape: Shape,
    pub(crate) span: Span,
}

impl Variable {
    pub(crate) fn required(&self) -> bool {
        self.fallback.is_none() && !matches!(self.shape, Shape::Optional(_))
    }

    pub(crate) fn parsed(&self) -> Option<&Type> {
        match &self.shape {
            Shape::Live => None,
            Shape::Optional(inner) | Shape::Direct(inner) if !is_string(inner) => Some(inner),
            _ => None,
        }
    }
}

pub(crate) fn variable(field: &Field) -> syn::Result<Variable> {
    let ident = field
        .ident
        .clone()
        .ok_or_else(|| syn::Error::new_spanned(field, "an Env struct has named fields."))?;
    let span = ident.span();
    let shape = shape(&field.ty)?;

    let mut key = ident.to_string().to_uppercase();
    let mut class = if matches!(shape, Shape::Live) {
        Class::Secret
    } else {
        Class::Plain
    };
    let mut folders: Option<Vec<String>> = None;
    let mut fallback = None;
    let description = description(field)?;

    for entry in entries(&field.attrs)? {
        let name = entry.name.to_string();
        match name.as_str() {
            "key" => key = entry.literal()?.to_string(),
            "default" => fallback = Some(entry.literal()?.to_string()),
            "folders" => folders = Some(entry.list()?.to_vec()),
            "secret" => {
                return Err(refused(entry.span(), &key, "is tagged secret. The secret class is declared by the field's type: make it an ocel::Secret and drop the attribute."))
            }
            "sensitive" if matches!(shape, Shape::Live) => {
                return Err(refused(entry.span(), &key, "is an ocel::Secret tagged 'sensitive'. A Secret field is always the secret class; drop the attribute."))
            }
            "sensitive" => class = Class::Sensitive,
            _ => {
                return Err(refused(
                    entry.span(),
                    &key,
                    &format!("has an unknown attribute '{name}'. The attributes are sensitive, key = \"<KEY>\", default = <VALUE> and folders = [\"<PATH>\"]."),
                ))
            }
        }
    }

    check(
        span,
        &key,
        class,
        folders.as_deref(),
        fallback.is_some(),
        &shape,
    )?;
    Ok(Variable {
        ident,
        key,
        class,
        folders: folders.unwrap_or_default(),
        fallback,
        description,
        shape,
        span,
    })
}

fn description(field: &Field) -> syn::Result<Option<String>> {
    let mut lines = Vec::new();
    for attribute in &field.attrs {
        if !attribute.path().is_ident("doc") {
            continue;
        }
        let syn::Meta::NameValue(value) = &attribute.meta else {
            continue;
        };
        let syn::Expr::Lit(literal) = &value.value else {
            continue;
        };
        let syn::Lit::Str(text) = &literal.lit else {
            continue;
        };
        lines.push(text.value().trim().to_string());
    }
    let description = lines.join("\n");
    if description.is_empty() {
        return Ok(None);
    }
    if description.len() > 120 {
        return Err(syn::Error::new_spanned(
            field,
            "an environment-variable description is at most 120 bytes.",
        ));
    }
    if description.chars().any(char::is_control) {
        return Err(syn::Error::new_spanned(
            field,
            "an environment-variable description is one line and has no control characters.",
        ));
    }
    Ok(Some(description))
}

fn check(
    span: Span,
    key: &str,
    class: Class,
    folders: Option<&[String]>,
    has_default: bool,
    shape: &Shape,
) -> syn::Result<()> {
    if !usable(key) {
        return Err(refused(span, key, "is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore."));
    }
    if key == URL_KEY {
        return Err(refused(span, key, "is written by Ocel for every app, from the hostname the deploy serves it on, so a declared one would be overwritten before anything read it. Read it with ocel::deployment_url()."));
    }
    if class == Class::Plain && key.starts_with(RESERVED_PREFIX) {
        return Err(refused(span, key, &format!("starts with the reserved prefix {RESERVED_PREFIX}. A '{}' variable is delivered under its own name, so Ocel would overwrite it.", class.name())));
    }
    if has_default && matches!(shape, Shape::Live) {
        return Err(refused(span, key, "is a Secret with a default. A live value must fail loudly when it is missing rather than fall back."));
    }
    if let Shape::Optional(inner) = shape {
        if is_secret(inner) {
            return Err(refused(span, key, "is an optional Secret. A live value must fail loudly when it is missing rather than fall back; declare it as an ocel::Secret."));
        }
    }
    if let Some(problem) = folders.and_then(scope_problem) {
        return Err(refused(
            span,
            key,
            &format!("has an unusable folder scope: {problem}"),
        ));
    }
    Ok(())
}

pub(crate) fn refused(span: Span, key: &str, detail: &str) -> syn::Error {
    syn::Error::new(span, format!("'{key}' {detail}"))
}

fn usable(key: &str) -> bool {
    let mut bytes = key.bytes();
    let Some(first) = bytes.next() else {
        return false;
    };
    if !first.is_ascii_uppercase() && first != b'_' {
        return false;
    }
    bytes.all(|byte| byte.is_ascii_uppercase() || byte.is_ascii_digit() || byte == b'_')
}

fn scope_problem(folders: &[String]) -> Option<String> {
    if folders.is_empty() {
        return Some("an empty folder scope says nothing. Leave 'folders' off to keep the variable at the project root.".to_string());
    }
    let mut seen: Vec<&String> = Vec::new();
    for folder in folders {
        if seen.contains(&folder) {
            return Some(format!("folder '{folder}' is named twice. A scoped variable holds one value per folder it names."));
        }
        seen.push(folder);
        if let Some(problem) = folder_problem(folder) {
            return Some(format!("folder '{folder}': {problem}"));
        }
    }
    None
}

fn folder_problem(folder: &str) -> Option<&'static str> {
    if !folder.starts_with('/') {
        Some("a folder path must start with '/'.")
    } else if folder == "/" {
        Some("'/' is the project root, which is what an unscoped variable already uses. Leave 'folders' off instead.")
    } else if folder.ends_with('/') {
        Some("a folder path must not end with '/'.")
    } else if folder.contains("//") {
        Some("a folder path has no empty segments.")
    } else if folder.contains('#') {
        Some("a folder path may not contain '#'.")
    } else {
        None
    }
}

fn shape(ty: &Type) -> syn::Result<Shape> {
    if is_secret(ty) {
        return Ok(Shape::Live);
    }
    if let Some(inner) = option_argument(ty) {
        return Ok(Shape::Optional(inner));
    }
    Ok(Shape::Direct(ty.clone()))
}

fn option_argument(ty: &Type) -> Option<Type> {
    let segment = last_segment(ty)?;
    if segment.ident != "Option" {
        return None;
    }
    let PathArguments::AngleBracketed(arguments) = &segment.arguments else {
        return None;
    };
    arguments.args.iter().find_map(|argument| match argument {
        GenericArgument::Type(inner) => Some(inner.clone()),
        _ => None,
    })
}

fn last_segment(ty: &Type) -> Option<&syn::PathSegment> {
    match ty {
        Type::Path(path) if path.qself.is_none() => path.path.segments.last(),
        _ => None,
    }
}

fn named(ty: &Type, name: &str) -> bool {
    last_segment(ty).is_some_and(|segment| segment.ident == name)
}

fn is_secret(ty: &Type) -> bool {
    named(ty, "Secret")
}

fn is_string(ty: &Type) -> bool {
    named(ty, "String")
}

pub(crate) fn is_bool(ty: &Type) -> bool {
    named(ty, "bool")
}

#[cfg(test)]
mod tests {
    use super::{variable, Class, Shape, Variable};
    use syn::parse::Parser;
    use syn::Field;

    fn parsed(source: &str) -> Result<Variable, String> {
        let field = Field::parse_named
            .parse_str(source)
            .expect("a named struct field");
        variable(&field).map_err(|err| err.to_string())
    }

    fn ok(source: &str) -> Variable {
        parsed(source).unwrap_or_else(|err| panic!("{source}: {err}"))
    }

    fn err(source: &str) -> String {
        parsed(source)
            .err()
            .unwrap_or_else(|| panic!("{source} was accepted"))
    }

    #[test]
    fn a_key_defaults_to_the_field_name_upper_cased() {
        assert_eq!(ok("pub database_name: String").key, "DATABASE_NAME");
        assert_eq!(ok(r#"#[ocel(key = "FLAG")] pub flag: bool"#).key, "FLAG");
    }

    #[test]
    fn a_control_character_in_a_doc_comment_is_refused() {
        assert!(err("/// first\n/// second\npub api_key: String").contains("one line"));
    }

    #[test]
    fn a_doc_comment_is_the_description() {
        assert_eq!(
            ok("/// The connection string\npub database_url: String")
                .description
                .as_deref(),
            Some("The connection string")
        );
    }

    #[test]
    fn a_field_is_plain_unless_it_says_otherwise() {
        assert!(ok("pub name: String").class == Class::Plain);
        assert!(ok("#[ocel(sensitive)] pub api_key: String").class == Class::Sensitive);
        assert!(ok("pub signing_key: ocel::Secret").class == Class::Secret);
    }

    #[test]
    fn a_field_is_required_unless_it_has_a_default_or_is_optional() {
        assert!(ok("pub name: String").required());
        assert!(!ok("#[ocel(default = 3000)] pub port: u16").required());
        assert!(!ok("pub timeout: Option<u64>").required());
    }

    #[test]
    fn every_type_but_a_string_and_a_secret_carries_a_schema() {
        assert!(ok("pub name: String").parsed().is_none());
        assert!(ok("pub signing_key: ocel::Secret").parsed().is_none());
        assert!(ok("pub timeout: Option<String>").parsed().is_none());
        assert!(ok("pub port: u16").parsed().is_some());
        assert!(ok("pub timeout: Option<u64>").parsed().is_some());
    }

    #[test]
    fn a_secret_is_live_and_an_option_carries_its_inner_type() {
        assert!(matches!(
            ok("pub signing_key: ocel::Secret").shape,
            Shape::Live
        ));
        assert!(matches!(
            ok("pub timeout: Option<u64>").shape,
            Shape::Optional(_)
        ));
        assert!(matches!(ok("pub port: u16").shape, Shape::Direct(_)));
    }

    #[test]
    fn a_folder_scope_comes_back_as_written() {
        let scoped = ok(r#"#[ocel(folders = ["/apps/web", "/apps/api"])] pub flag: bool"#);
        assert_eq!(scoped.folders, ["/apps/web", "/apps/api"]);
    }

    #[test]
    fn a_key_no_environment_accepts_is_refused() {
        assert_eq!(
            err(r#"#[ocel(key = "lower")] pub k: String"#),
            "'lower' is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore."
        );
    }

    #[test]
    fn the_url_ocel_writes_is_refused_under_every_class() {
        for source in [
            r#"#[ocel(key = "OCEL_URL")] pub url: String"#,
            r#"#[ocel(key = "OCEL_URL", sensitive)] pub url: String"#,
            r#"#[ocel(key = "OCEL_URL")] pub url: ocel::Secret"#,
        ] {
            assert!(
                err(source).contains("Read it with ocel::deployment_url()."),
                "{source} was not sent to deployment_url"
            );
        }
    }

    #[test]
    fn the_reserved_prefix_is_refused_for_a_plain_variable_and_taken_for_the_others() {
        assert_eq!(
            err(r#"#[ocel(key = "OCEL_REGION")] pub region: String"#),
            "'OCEL_REGION' starts with the reserved prefix OCEL_. A 'plain' variable is delivered under its own name, so Ocel would overwrite it."
        );
        assert_eq!(
            ok(r#"#[ocel(key = "OCEL_REGION", sensitive)] pub region: String"#).key,
            "OCEL_REGION"
        );
    }

    #[test]
    fn a_secret_that_could_fall_back_or_go_missing_quietly_is_refused() {
        assert_eq!(
            err(r#"#[ocel(default = "x")] pub token: ocel::Secret"#),
            "'TOKEN' is a Secret with a default. A live value must fail loudly when it is missing rather than fall back."
        );
        assert_eq!(
            err("pub token: Option<ocel::Secret>"),
            "'TOKEN' is an optional Secret. A live value must fail loudly when it is missing rather than fall back; declare it as an ocel::Secret."
        );
        assert_eq!(
            err("#[ocel(sensitive)] pub token: ocel::Secret"),
            "'TOKEN' is an ocel::Secret tagged 'sensitive'. A Secret field is always the secret class; drop the attribute."
        );
    }

    #[test]
    fn the_secret_class_is_declared_by_the_type_and_never_by_an_attribute() {
        assert_eq!(
            err("#[ocel(secret)] pub token: String"),
            "'TOKEN' is tagged secret. The secret class is declared by the field's type: make it an ocel::Secret and drop the attribute."
        );
    }

    #[test]
    fn an_attribute_ocel_does_not_know_is_refused_by_name() {
        assert!(
            err("#[ocel(optional)] pub k: String").contains("has an unknown attribute 'optional'")
        );
    }

    #[test]
    fn a_folder_scope_nothing_could_hold_a_value_for_is_refused() {
        for (source, want) in [
            (
                r#"#[ocel(folders = [])] pub k: bool"#,
                "'K' has an unusable folder scope: an empty folder scope says nothing. Leave 'folders' off to keep the variable at the project root.",
            ),
            (
                r#"#[ocel(folders = ["apps/web"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder 'apps/web': a folder path must start with '/'.",
            ),
            (
                r#"#[ocel(folders = ["/"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder '/': '/' is the project root, which is what an unscoped variable already uses. Leave 'folders' off instead.",
            ),
            (
                r#"#[ocel(folders = ["/apps/"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder '/apps/': a folder path must not end with '/'.",
            ),
            (
                r#"#[ocel(folders = ["/apps//web"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder '/apps//web': a folder path has no empty segments.",
            ),
            (
                r#"#[ocel(folders = ["/apps/we#b"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder '/apps/we#b': a folder path may not contain '#'.",
            ),
            (
                r#"#[ocel(folders = ["/apps/web", "/apps/web"])] pub k: bool"#,
                "'K' has an unusable folder scope: folder '/apps/web' is named twice. A scoped variable holds one value per folder it names.",
            ),
        ] {
            assert_eq!(err(source), want, "in {source}");
        }
    }
}

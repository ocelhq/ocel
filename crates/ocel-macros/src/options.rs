use crate::attribute::Entry;
use proc_macro2::TokenStream;
use quote::quote;

pub(crate) fn parse_duration(entry: &Entry) -> syn::Result<TokenStream> {
    let (seconds, nanos) = read_duration(entry)?;
    Ok(quote!(::core::option::Option::Some(::core::time::Duration::new(#seconds, #nanos))))
}

pub(crate) fn read_duration(entry: &Entry) -> syn::Result<(u64, u32)> {
    let text = entry.read_literal()?;
    parse_duration_text(text).ok_or_else(|| {
        syn::Error::new(
            entry.span(),
            format!(
                "'{}' is \"{text}\", and a duration is a whole number followed by ms, s, m, h or d, such as \"500ms\", \"30s\" or \"5m\".",
                entry.name
            ),
        )
    })
}

fn parse_duration_text(text: &str) -> Option<(u64, u32)> {
    let split = text.find(|character: char| !character.is_ascii_digit())?;
    let (number, unit) = text.split_at(split);
    let number: u64 = number.parse().ok()?;
    let seconds_per_unit = match unit {
        "ms" => return Some((number / 1000, ((number % 1000) * 1_000_000) as u32)),
        "s" => 1,
        "m" => 60,
        "h" => 3600,
        "d" => 86400,
        _ => return None,
    };
    Some((number.checked_mul(seconds_per_unit)?, 0))
}

pub(crate) fn parse_count(entry: &Entry) -> syn::Result<i32> {
    entry
        .read_literal()?
        .parse::<i32>()
        .ok()
        .filter(|n| *n >= 0)
        .ok_or_else(|| {
            syn::Error::new(
                entry.span(),
                format!(
                    "'{}' wants a whole number: {} = <NUMBER>.",
                    entry.name, entry.name
                ),
            )
        })
}

pub(crate) fn parse_batch_size(entry: &Entry) -> syn::Result<i32> {
    match parse_count(entry)? {
        0 => Err(syn::Error::new(
            entry.span(),
            format!(
                "'{}' is 0, and a batch holds at least one message.",
                entry.name
            ),
        )),
        size => Ok(size),
    }
}

pub(crate) fn parse_string(entry: &Entry) -> syn::Result<String> {
    Ok(entry.read_literal()?.to_string())
}

pub(crate) fn parse_retry(entry: &Entry) -> syn::Result<TokenStream> {
    let mut max_attempts = 0;
    let mut min_delay = quote_none();
    let mut max_delay = quote_none();
    for inner in entry.read_nested()? {
        match inner.name.to_string().as_str() {
            "max_attempts" => max_attempts = parse_count(inner)?,
            "min_delay" => min_delay = parse_duration(inner)?,
            "max_delay" => max_delay = parse_duration(inner)?,
            other => {
                return Err(refuse_unknown_attribute(
                    inner,
                    other,
                    "retry",
                    "max_attempts = <NUMBER>, min_delay = \"<DURATION>\" and max_delay = \"<DURATION>\"",
                ))
            }
        }
    }
    Ok(quote! {
        ::core::option::Option::Some(::ocel::Retry {
            max_attempts: #max_attempts,
            min_delay: #min_delay,
            max_delay: #max_delay,
        })
    })
}

pub(crate) fn parse_batch(entry: &Entry) -> syn::Result<TokenStream> {
    let mut size = None;
    let mut timeout = quote_none();
    for inner in entry.read_nested()? {
        match inner.name.to_string().as_str() {
            "size" => size = Some(parse_batch_size(inner)?),
            "timeout" => timeout = parse_duration(inner)?,
            other => {
                return Err(refuse_unknown_attribute(
                    inner,
                    other,
                    "batch",
                    "size = <NUMBER> and timeout = \"<DURATION>\"",
                ))
            }
        }
    }
    let Some(size) = size else {
        return Err(syn::Error::new(
            entry.span(),
            "batch(...) names how many messages a batch holds: batch(size = <NUMBER>).",
        ));
    };
    Ok(quote_batch(size, timeout))
}

pub(crate) fn quote_batch(size: i32, timeout: TokenStream) -> TokenStream {
    quote! {
        ::core::option::Option::Some(::ocel::Batch { size: #size, timeout: #timeout })
    }
}

pub(crate) fn parse_lanes(entry: &Entry) -> syn::Result<TokenStream> {
    let mut lanes = Vec::new();
    for lane in entry.read_list()? {
        lanes.push(match lane.as_str() {
            "high" => quote!(::ocel::Lane::High),
            "default" => quote!(::ocel::Lane::Default),
            "low" => quote!(::ocel::Lane::Low),
            other => {
                return Err(syn::Error::new(
                    entry.span(),
                    format!(
                    "'lanes' names \"{other}\", and a lane is \"high\", \"default\" or \"low\"."
                ),
                ))
            }
        });
    }
    Ok(quote!(&[#(#lanes),*]))
}

pub(crate) fn quote_none() -> TokenStream {
    quote!(::core::option::Option::None)
}

pub(crate) fn refuse_unknown_attribute(
    entry: &Entry,
    name: &str,
    owner: &str,
    known: &str,
) -> syn::Error {
    syn::Error::new(
        entry.span(),
        format!("{owner} has an unknown attribute '{name}'. Its attributes are {known}."),
    )
}

#[cfg(test)]
mod tests {
    use super::parse_duration_text;

    #[test]
    fn a_duration_is_a_whole_number_and_a_unit() {
        assert_eq!(parse_duration_text("500ms"), Some((0, 500_000_000)));
        assert_eq!(parse_duration_text("1500ms"), Some((1, 500_000_000)));
        assert_eq!(parse_duration_text("30s"), Some((30, 0)));
        assert_eq!(parse_duration_text("5m"), Some((300, 0)));
        assert_eq!(parse_duration_text("2h"), Some((7200, 0)));
        assert_eq!(parse_duration_text("1d"), Some((86400, 0)));
    }

    #[test]
    fn a_duration_without_a_known_unit_or_a_whole_number_is_refused() {
        for text in ["30", "s", "1.5s", "5 m", "5w", "-1s", ""] {
            assert_eq!(parse_duration_text(text), None, "{text} parsed");
        }
    }
}

use proc_macro2::Span;

pub(crate) fn source(span: Span) -> (String, u32) {
    let span = span.unwrap();
    let file = span
        .local_file()
        .map_or_else(|| span.file(), |path| path.display().to_string());
    (file, u32::try_from(span.line()).unwrap_or_default())
}

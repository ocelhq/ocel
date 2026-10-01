use serde::{Serialize, Serializer};
use std::fmt;

/// Why the realtime handler denied an op, or why [`Realtime::publish`](super::Realtime::publish)
/// refused one. It is written as its kebab-case code, such as `unknown-pattern`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DenialCode {
    /// `invalid-op`: the op is no JSON object, has a key besides `op`, `pattern`, `params`
    /// and `body`, or is a subscribe with a `body`.
    InvalidOp,
    /// `unknown-op`: the op is neither `subscribe` nor `publish`.
    UnknownOp,
    /// `unknown-pattern`: no channel of the resource has the pattern.
    UnknownPattern,
    /// `no-publish-rule`: a browser publish on a channel declared without `publish`.
    NoPublishRule,
    /// `invalid-params`: the params are not an object of strings.
    InvalidParams,
    /// `missing-param`: a param the pattern needs is absent.
    MissingParam,
    /// `unknown-param`: a param the pattern does not name.
    UnknownParam,
    /// `empty-value`: a param is the empty string.
    EmptyValue,
    /// `value-too-long`: a param is over 30 bytes.
    ValueTooLong,
    /// `invalid-body`: the event is absent, is not the channel's event type, or fails the
    /// channel's JSON Schema.
    InvalidBody,
    /// `body-too-large`: the encoded event is over 240 KiB.
    BodyTooLarge,
    /// `unauthenticated`: the channel has a rule and `authorize` answered nobody.
    Unauthenticated,
    /// `forbidden`: the rule answered false.
    Forbidden,
    /// `rule-error`: the rule failed or panicked.
    RuleError,
    /// `publish-failed`: the transport refused the publish, could not be reached, or did
    /// not answer within 10 seconds.
    PublishFailed,
}

impl fmt::Display for DenialCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::InvalidOp => "invalid-op",
            Self::UnknownOp => "unknown-op",
            Self::UnknownPattern => "unknown-pattern",
            Self::NoPublishRule => "no-publish-rule",
            Self::InvalidParams => "invalid-params",
            Self::MissingParam => "missing-param",
            Self::UnknownParam => "unknown-param",
            Self::EmptyValue => "empty-value",
            Self::ValueTooLong => "value-too-long",
            Self::InvalidBody => "invalid-body",
            Self::BodyTooLarge => "body-too-large",
            Self::Unauthenticated => "unauthenticated",
            Self::Forbidden => "forbidden",
            Self::RuleError => "rule-error",
            Self::PublishFailed => "publish-failed",
        })
    }
}

impl Serialize for DenialCode {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.collect_str(self)
    }
}

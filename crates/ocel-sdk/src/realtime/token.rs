use super::Realtime;
use crate::proto::app::realtime::v1::{Grant, MintRequest};
use serde::Serialize;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Operation {
    Subscribe,
    Publish,
}

#[derive(Serialize)]
pub(crate) struct MintedToken {
    pub(crate) token: String,
    #[serde(rename = "expiresAt")]
    pub(crate) expires_at: i64,
}

pub(crate) struct Minted {
    pub(crate) tokens: Vec<(String, String)>,
    pub(crate) expires_at: i64,
}

pub(crate) async fn mint_tokens(
    rt: &Realtime,
    subject: &str,
    grants: Vec<Grant>,
) -> Result<Minted, String> {
    let asked = grants.len();
    let ttl = rt.inner.token_ttl;
    let response = rt
        .connect_runtime()
        .map_err(|err| err.to_string())?
        .mint(MintRequest {
            realtime: rt.inner.name.clone(),
            subject: subject.to_string(),
            ttl: buffa_types::google::protobuf::Duration {
                seconds: ttl.as_secs() as i64,
                nanos: ttl.subsec_nanos() as i32,
                ..Default::default()
            }
            .into(),
            grants,
            ..Default::default()
        })
        .await
        .map_err(|err| err.to_string())?;
    let minted = response.into_owned();
    if minted.tokens.len() != asked {
        return Err(format!(
            "the runtime minted {} tokens for {asked} grants",
            minted.tokens.len()
        ));
    }
    Ok(Minted {
        expires_at: minted.expires_at.as_option().map_or(0, |at| at.seconds),
        tokens: minted
            .tokens
            .into_iter()
            .map(|token| (token.token, token.channel))
            .collect(),
    })
}

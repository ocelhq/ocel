use super::Realtime;
use crate::proto::app::realtime::v1::MintRequest;
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

pub(crate) struct SubscribeToken {
    pub(crate) token: String,
    pub(crate) channel: String,
}

pub(crate) struct Minted {
    pub(crate) connect_token: Option<String>,
    pub(crate) subscribe_tokens: Vec<SubscribeToken>,
    pub(crate) expires_at: i64,
}

pub(crate) async fn mint_tokens(
    rt: &Realtime,
    subject: &str,
    connect: bool,
    subscribe_channels: Vec<String>,
) -> Result<Minted, String> {
    let asked = subscribe_channels.len();
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
            connect,
            subscribe_channels,
            ..Default::default()
        })
        .await
        .map_err(|err| err.to_string())?;
    let minted = response.into_owned();
    if minted.subscribe_tokens.len() != asked {
        return Err(format!(
            "the runtime minted {} subscribe tokens for {asked} channels",
            minted.subscribe_tokens.len()
        ));
    }
    if connect && minted.connect_token.is_empty() {
        return Err("the runtime minted no connect token".to_string());
    }
    let expires_at = minted
        .expires_at
        .as_option()
        .map(|at| at.seconds)
        .ok_or("the runtime minted tokens without an expiry")?;
    Ok(Minted {
        connect_token: connect.then_some(minted.connect_token),
        subscribe_tokens: minted
            .subscribe_tokens
            .into_iter()
            .map(|minted| SubscribeToken {
                token: minted.token,
                channel: minted.channel,
            })
            .collect(),
        expires_at,
    })
}

use crate::Error;
use connectrpc::client::ClientConfig;

const RUNTIME_ADDRESS_ENV: &str = "OCEL_RUNTIME_ADDRESS";
const SESSION_TOKEN_ENV: &str = "OCEL_SESSION_TOKEN";

pub(crate) fn read_client_config() -> Result<ClientConfig, Error> {
    let address = std::env::var(RUNTIME_ADDRESS_ENV).unwrap_or_default();
    if address.is_empty() {
        return Err(Error::UnreachableRuntime);
    }
    let Ok(base) = address.trim_end_matches('/').parse() else {
        return Err(Error::RuntimeAddress { address });
    };
    let token = std::env::var(SESSION_TOKEN_ENV).unwrap_or_default();
    if token.is_empty() {
        return Err(Error::UntrustedRuntime);
    }
    Ok(ClientConfig::new(base).with_default_header("authorization", format!("Bearer {token}")))
}

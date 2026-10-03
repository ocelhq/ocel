use crate::proto::common::bindings::v1::{RealtimeProperties, RealtimeTransport};
use serde::Serialize;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Transport {
    AppsyncEvents,
    OcelGateway,
}

pub(crate) fn read_transport(properties: &RealtimeProperties) -> Option<Transport> {
    match properties.transport.as_known()? {
        RealtimeTransport::REALTIME_TRANSPORT_APPSYNC_EVENTS => Some(Transport::AppsyncEvents),
        RealtimeTransport::REALTIME_TRANSPORT_OCEL_GATEWAY => Some(Transport::OcelGateway),
        _ => None,
    }
}

use crate::proto::app::topic::v1::Lane as WireLane;

/// The lane a message or run waits in. Reads are weighted 6:3:1 across high, default and
/// low, so a busy lane slows the others without starving them.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub enum Lane {
    /// Read six times as often as low.
    High,
    /// The lane a message waits in unless it says otherwise.
    #[default]
    Default,
    /// Read least often.
    Low,
}

impl Lane {
    pub(crate) fn to_wire(self) -> WireLane {
        match self {
            Self::High => WireLane::LANE_HIGH,
            Self::Default => WireLane::LANE_DEFAULT,
            Self::Low => WireLane::LANE_LOW,
        }
    }
}

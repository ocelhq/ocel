import json
from pathlib import Path

import pytest

from ocel._realtime_wire import ChannelPattern, encode_wire_channel

VECTORS = json.loads(
    (Path(__file__).parents[3] / "proto" / "realtime" / "vectors.json").read_text()
)


@pytest.mark.parametrize("vector", VECTORS["wire"], ids=lambda vector: vector["name"])
def test_every_wire_vector_encodes_to_its_channel_or_is_refused_for_its_reason(vector):
    pattern = ChannelPattern(vector["pattern"])

    channel, refused = encode_wire_channel(
        vector["namespace"], pattern, vector["params"], vector["wildcard"]
    )

    assert (channel, refused) == (vector.get("channel"), vector.get("error"))


@pytest.mark.parametrize(
    ("written", "reason"),
    [
        ("", "empty"),
        ("a/b/c/d/e", "at most 4"),
        ("orders//x", "empty segment"),
        ("orders/:1x", "no parameter"),
        ("orders_x", "no literal"),
        ("rooms/:id/:id", "twice"),
    ],
)
def test_a_channel_pattern_outside_the_grammar_is_refused_saying_why(written, reason):
    with pytest.raises(ValueError, match=reason):
        ChannelPattern(written)

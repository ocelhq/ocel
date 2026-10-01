import base64
import json
from pathlib import Path

import pytest

from ocel._realtime_token import sign_token

VECTORS = json.loads(
    (Path(__file__).parents[3] / "proto" / "realtime" / "vectors.json").read_text()
)


@pytest.mark.parametrize("vector", VECTORS["tokens"]["mint"], ids=lambda vector: vector["name"])
def test_every_mint_vector_signs_to_its_token_byte_for_byte(vector):
    signing_key = base64.b64decode(VECTORS["tokens"]["signingKey"])

    assert sign_token(signing_key, vector["header"], vector["claims"]) == vector["token"]

import importlib.util

from ocel.gen.app.resources.v1.resources_pb import ResourceType

_INFRA = """
import ocel

uploads = ocel.bucket("uploads")
db = ocel.postgres("main")
cache = ocel.kv("cache")
visits = cache.counter("visits", "visits/:page")
names = cache.text("names", "names/:id")
"""


def _import_as(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_a_module_imported_under_two_names_sends_the_same_declarations_each_time(
    collector, tmp_path
):
    infra = tmp_path / "infra.py"
    infra.write_text(_INFRA)

    _import_as("infra", infra)
    first = [body for _, _, body in collector.declares]
    _import_as("app.infra", infra)
    second = [body for _, _, body in collector.declares][len(first) :]

    assert [d.resource.type for d in first] == [
        ResourceType.BUCKET,
        ResourceType.POSTGRES,
        ResourceType.KV,
        ResourceType.KV,
        ResourceType.KV,
    ]
    assert [d.to_binary() for d in second] == [d.to_binary() for d in first]

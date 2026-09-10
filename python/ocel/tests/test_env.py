import inspect
import logging
import os
from enum import Enum
from pathlib import Path

import pytest

import ocel
from ocel.env import _Variable
from ocel.gen.app.resources.v1.variables_pb import VariableCell, VariableClass, VariableProblem


def define(source: str, filename: str) -> dict:
    namespace = {"ocel": ocel, "__name__": __name__}
    exec(compile(source, filename, "exec"), namespace)
    return namespace


def test_a_key_no_variable_could_carry_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            name: str = ocel.var(key="lower-case")

    assert str(raised.value) == (
        "'lower-case' is not a usable variable name: use upper-case letters, digits and "
        "underscores, starting with a letter or underscore."
    )


@pytest.mark.parametrize("description", ["two\nlines", "x" * 121])
def test_an_unusable_description_is_refused(description):
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            api_key: str = ocel.var(description=description)

    assert "unusable description" in str(raised.value)


def test_a_name_ocel_delivers_bare_is_refused_for_the_plain_class():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            ocel_thing: str

        _ = Env

    assert "reserved prefix OCEL_" in str(raised.value)


def test_a_name_ocel_delivers_bare_is_taken_under_a_class_delivered_namespaced(monkeypatch):
    monkeypatch.setenv("OCEL_VAR_OCEL_THING", "v")

    class Env(ocel.Env):
        thing: str = ocel.var(key="OCEL_THING", sensitive=True)

    assert Env().thing == "v"


@pytest.mark.parametrize(
    "declaration",
    [
        "ocel_url: str",
        "ocel_url: str = ocel.var(sensitive=True)",
        "ocel_url: ocel.Secret",
    ],
)
def test_the_deployment_url_is_refused_under_every_class(declaration):
    with pytest.raises(ocel.EnvDefinitionError) as raised:
        define(f"class Env(ocel.Env):\n    {declaration}\n", "/a.py")

    assert "Read it with `ocel.deployment_url()`" in str(raised.value)


def test_a_secret_with_a_default_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            signing_key: ocel.Secret = ocel.var(default="x")

    assert str(raised.value) == (
        "'SIGNING_KEY' is a Secret with a default. A live value must fail loudly when it "
        "is missing rather than fall back."
    )


def test_an_optional_secret_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            signing_key: ocel.Secret | None

        _ = Env

    assert str(raised.value) == (
        "'SIGNING_KEY' is an optional Secret. A live value must fail loudly when it is "
        "missing rather than fall back; declare it as an ocel.Secret."
    )


def test_a_secret_declared_sensitive_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            signing_key: ocel.Secret = ocel.var(sensitive=True)

    assert str(raised.value) == (
        "'SIGNING_KEY' is an ocel.Secret declared sensitive. A Secret attribute is always "
        "the secret class; drop the option."
    )


def test_two_attributes_naming_one_key_are_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            first: str = ocel.var(key="TWICE")
            second: str = ocel.var(key="TWICE")

    assert str(raised.value) == (
        "'TWICE' is declared by two attributes of the same class. A key is declared by "
        "exactly one attribute."
    )


def test_a_key_another_file_already_declared_is_refused():
    define("class Env(ocel.Env):\n    shared: str\n", "/one.py")

    with pytest.raises(ocel.EnvDefinitionError) as raised:
        define("class Other(ocel.Env):\n    shared: str\n", "/two.py")

    assert str(raised.value) == (
        "'SHARED' is already declared in /one.py. A key may be defined by exactly one file."
    )


def test_the_file_that_declared_a_key_declares_it_again(monkeypatch):
    monkeypatch.setenv("SHARED", "v")
    define("class Env(ocel.Env):\n    shared: str\n", "/one.py")

    namespace = define("class Again(ocel.Env):\n    shared: str\n", "/one.py")

    assert namespace["Again"]().shared == "v"


@pytest.mark.parametrize(
    ("folders", "problem"),
    [
        (["apps/web"], "must start with '/'"),
        (["/"], "'/' is the project root"),
        (["/apps/web/"], "must not end with '/'"),
        (["/apps//web"], "no empty segments"),
        (["/apps/web#1"], "may not contain '#'"),
        (["/apps/web", "/apps/web"], "is named twice"),
        ([], "an empty folder scope says nothing"),
    ],
)
def test_an_unusable_folder_scope_is_refused(folders, problem):
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            flag: str = ocel.var(folders=folders)

    assert str(raised.value).startswith("'FLAG' has an unusable folder scope: ")
    assert problem in str(raised.value)


def test_an_annotation_nothing_parses_a_value_into_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            tags: list[str]

        _ = Env

    assert str(raised.value) == (
        "'TAGS' is read into a list[str], which nothing parses a value into. Use a str, a "
        "bool, an ocel.Secret, any type whose constructor takes the delivered text, or "
        "'T | None' for an optional one."
    )


def test_a_union_of_more_than_one_type_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            port: int | str

        _ = Env

    assert "nothing parses a value into" in str(raised.value)


def test_a_default_the_annotation_rejects_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            port: int = "eighty"

    assert str(raised.value) == (
        "'PORT' has a default its own type rejects: invalid literal for int() with base 10: "
        "'eighty'."
    )


def test_a_default_of_another_type_than_the_annotation_is_refused():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            port: int = 80.5

    assert str(raised.value) == (
        "'PORT' has a default its own type rejects: a float where the annotation says int."
    )


def test_a_default_a_confidential_class_rejects_keeps_the_value_out_of_the_complaint():
    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Env(ocel.Env):
            limit: int = ocel.var(default="s3cret", sensitive=True)

    assert "s3cret" not in str(raised.value)
    assert str(raised.value) == (
        "'LIMIT' has a default its own type rejects: withheld, because a 'sensitive' "
        "value's parse message can quote the value itself."
    )


def test_a_class_of_a_class_of_env_is_refused():
    class Env(ocel.Env):
        name: str

    with pytest.raises(ocel.EnvDefinitionError) as raised:

        class Narrower(Env):
            other: str

        _ = Narrower

    assert str(raised.value) == (
        "Narrower extends Env, which declares variables of its own. A class declares the "
        "variables its own attributes name: subclass ocel.Env directly."
    )


def test_an_unannotated_attribute_declares_nothing(collector):
    class Env(ocel.Env):
        declared: str
        undeclared = "not a variable"

        def method(self):
            return self.declared

    assert [d.key for d in collector.declared_env().definitions] == ["DECLARED"]
    assert Env.undeclared == "not a variable"


def test_every_variable_reaches_the_dev_server_with_its_class_and_what_it_needs(collector):
    class Env(ocel.Env):
        plain: str
        sensitive: str = ocel.var(sensitive=True, description="Used to call Stripe")
        signing_key: ocel.Secret
        defaulted: int = 3
        optional: bool | None = None
        scoped: float = ocel.var(folders=["/apps/web", "/apps/api"])

    line = inspect.getsourcelines(Env)[1]
    definitions = collector.declared_env().definitions
    assert [d.key for d in definitions] == [
        "PLAIN",
        "SENSITIVE",
        "SIGNING_KEY",
        "DEFAULTED",
        "OPTIONAL",
        "SCOPED",
    ]
    assert [d.class_ for d in definitions] == [
        VariableClass.PLAIN,
        VariableClass.SENSITIVE,
        VariableClass.SECRET,
        VariableClass.PLAIN,
        VariableClass.PLAIN,
        VariableClass.PLAIN,
    ]
    assert [d.required for d in definitions] == [True, True, True, False, False, True]
    assert [d.has_schema for d in definitions] == [False, False, False, True, True, True]
    assert definitions[1].description == "Used to call Stripe"
    assert list(definitions[5].folders) == ["/apps/web", "/apps/api"]
    for definition in definitions:
        assert definition.source == f"{__file__}:{line}"


def test_a_required_key_the_store_holds_no_cell_for_is_reported(collector):
    collector.cells = [VariableCell(key="PRESENT", value="v")]

    class Env(ocel.Env):
        present: str
        missing: str

    _ = Env
    problems = collector.reported()
    assert len(problems) == 1
    assert problems[0].key == "MISSING"
    assert problems[0].kind is VariableProblem.Kind.MISSING
    assert problems[0].folder == ""


def test_a_scoped_key_is_reported_for_every_folder_the_store_has_no_cell_in(collector):
    collector.cells = [VariableCell(key="FLAG", folder="/apps/web", value="true")]

    class Env(ocel.Env):
        flag: bool = ocel.var(folders=["/apps/web", "/apps/api"])

    _ = Env
    problems = collector.reported()
    assert len(problems) == 1
    assert (problems[0].key, problems[0].folder) == ("FLAG", "/apps/api")


def test_a_stored_value_the_annotation_rejects_is_reported(collector):
    collector.cells = [
        VariableCell(key="PORT", value="eighty"),
        VariableCell(key="PORT", folder="/apps/web", value="80"),
    ]

    class Env(ocel.Env):
        port: int

    _ = Env
    problems = collector.reported()
    assert len(problems) == 1
    assert problems[0].key == "PORT"
    assert problems[0].kind is VariableProblem.Kind.INVALID
    assert problems[0].folder == ""
    assert "invalid literal for int()" in problems[0].detail


def test_a_stored_value_of_a_confidential_class_is_reported_without_quoting_it(collector):
    collector.cells = [VariableCell(key="LIMIT", value="s3cret")]

    class Env(ocel.Env):
        limit: int = ocel.var(sensitive=True)

    _ = Env
    detail = collector.reported()[0].detail
    assert "s3cret" not in detail
    assert detail == (
        "withheld, because a 'sensitive' value's parse message can quote the value itself"
    )


def test_nothing_is_reported_when_every_cell_satisfies_its_annotation(collector):
    collector.cells = [VariableCell(key="PORT", value="80"), VariableCell(key="NAME", value="n")]

    class Env(ocel.Env):
        port: int
        name: str
        optional: int | None = None
        defaulted: int = 1

    _ = Env
    assert collector.reported() == []


def test_a_live_cell_stands_without_a_value_and_is_never_checked(collector):
    collector.cells = [VariableCell(key="SIGNING_KEY")]

    class Env(ocel.Env):
        signing_key: ocel.Secret

    _ = Env
    assert collector.reported() == []


def test_the_namespaced_value_wins_over_a_bare_name_of_the_same_key(monkeypatch):
    monkeypatch.setenv("OCEL_VAR_NAME", "baked")
    monkeypatch.setenv("NAME", "bare")

    class Env(ocel.Env):
        name: str

    assert Env().name == "baked"


def test_a_default_stands_in_only_where_nothing_is_set(monkeypatch):
    monkeypatch.setenv("NAME", "set")
    monkeypatch.delenv("UNSET_NAME", raising=False)

    class Env(ocel.Env):
        name: str = "d"
        unset_name: str = "d"
        port: int = ocel.var(default="8080")

    env = Env()
    assert (env.name, env.unset_name, env.port) == ("set", "d", 8080)


def test_an_optional_nothing_is_set_for_reads_as_none(monkeypatch):
    monkeypatch.delenv("TIMEOUT", raising=False)

    class Env(ocel.Env):
        timeout: float | None = None

    assert Env().timeout is None


@pytest.mark.parametrize(
    ("raw", "want"),
    [
        ("1", True),
        ("t", True),
        ("T", True),
        ("TRUE", True),
        ("true", True),
        ("True", True),
        ("0", False),
        ("f", False),
        ("F", False),
        ("FALSE", False),
        ("false", False),
        ("False", False),
    ],
)
def test_a_bool_takes_the_set_every_language_takes(monkeypatch, raw, want):
    monkeypatch.setenv("FLAG", raw)

    class Env(ocel.Env):
        flag: bool

    assert Env().flag is want


@pytest.mark.parametrize("raw", ["yes", "TrUe", "2", ""])
def test_a_bool_refuses_anything_outside_that_set(monkeypatch, raw):
    monkeypatch.setenv("FLAG", raw)

    class Env(ocel.Env):
        flag: bool

    with pytest.raises(ocel.EnvValueError) as raised:
        _ = Env().flag
    assert "want one of 1 t T TRUE true True 0 f F FALSE false False" in str(raised.value)


class Level(Enum):
    DEBUG = "debug"
    INFO = "info"


def test_every_annotation_parses_the_delivered_text_into_itself(monkeypatch):
    monkeypatch.setenv("NAME", "n")
    monkeypatch.setenv("PORT", "-7")
    monkeypatch.setenv("RATIO", "1.5")
    monkeypatch.setenv("HOME_DIR", "/srv/app")
    monkeypatch.setenv("LEVEL", "info")

    class Env(ocel.Env):
        name: str
        port: int
        ratio: float
        home_dir: Path
        level: Level

    env = Env()
    assert (env.name, env.port, env.ratio) == ("n", -7, 1.5)
    assert env.home_dir == Path("/srv/app")
    assert env.level is Level.INFO


def test_an_int_refuses_text_no_integer_stands_behind(monkeypatch):
    monkeypatch.setenv("PORT", "80.5")

    class Env(ocel.Env):
        port: int

    with pytest.raises(ocel.EnvValueError):
        _ = Env().port


def test_a_variable_nothing_is_set_for_names_the_command_that_sets_one(monkeypatch):
    monkeypatch.delenv("NOTHING_SET", raising=False)

    class Env(ocel.Env):
        nothing_set: str

    with pytest.raises(ocel.EnvValueError) as raised:
        _ = Env().nothing_set
    assert str(raised.value) == (
        "'NOTHING_SET' has no value. Set one with `ocel env set NOTHING_SET=<VALUE>`."
    )


def test_a_value_its_annotation_rejects_names_the_command_that_fixes_it(monkeypatch):
    monkeypatch.setenv("PORT", "eighty")

    class Env(ocel.Env):
        port: int

    with pytest.raises(ocel.EnvValueError) as raised:
        _ = Env().port
    assert str(raised.value) == (
        "'PORT' is set but does not satisfy its type: invalid literal for int() with base "
        "10: 'eighty'. Fix it with `ocel env set PORT=<VALUE>`."
    )


def test_a_confidential_value_stays_out_of_the_error_the_read_raises(monkeypatch):
    monkeypatch.setenv("LIMIT", "s3cret")

    class Env(ocel.Env):
        limit: int = ocel.var(sensitive=True)

    with pytest.raises(ocel.EnvValueError) as raised:
        _ = Env().limit
    assert "s3cret" not in str(raised.value)
    assert "withheld" in str(raised.value)


def test_a_variable_this_apps_binding_leaves_out_of_scope_is_refused(monkeypatch):
    monkeypatch.setenv("OCEL_APP_FOLDER", "/apps/api")
    monkeypatch.setenv("FLAG", "true")

    class Env(ocel.Env):
        flag: bool = ocel.var(folders=["/apps/web"])

    with pytest.raises(ocel.EnvScopeError) as raised:
        _ = Env().flag
    assert str(raised.value) == (
        "'FLAG' is scoped to /apps/web, but this app is bound to /apps/api. Bind this app "
        "to one of those folders in ocel.json, or widen the variable's scope."
    )
    assert (raised.value.key, raised.value.binding) == ("FLAG", "/apps/api")
    assert raised.value.folders == ("/apps/web",)


def test_a_variable_scoped_to_the_folder_this_app_is_bound_to_is_read(monkeypatch):
    monkeypatch.setenv("OCEL_APP_FOLDER", "/apps/web")
    monkeypatch.setenv("FLAG", "true")

    class Env(ocel.Env):
        flag: bool = ocel.var(folders=["/apps/api", "/apps/web"])

    assert Env().flag is True


def test_a_value_is_read_once_and_stands_for_the_life_of_the_instance(monkeypatch):
    monkeypatch.setenv("PORT", "8080")

    class Env(ocel.Env):
        port: int

    env = Env()
    assert env.port == 8080
    monkeypatch.setenv("PORT", "9090")
    assert env.port == 8080
    assert Env().port == 9090


def test_a_variable_reached_through_the_class_hands_back_what_declares_it():
    class Env(ocel.Env):
        port: int = 3000

    assert isinstance(Env.port, _Variable)


def test_a_secret_resolves_on_every_read(monkeypatch):
    monkeypatch.setenv("OCEL_VAR_SIGNING_KEY", "first")

    class Env(ocel.Env):
        signing_key: ocel.Secret

    key = Env().signing_key
    assert key.key == "SIGNING_KEY"
    assert key.value == "first"
    monkeypatch.setenv("OCEL_VAR_SIGNING_KEY", "rotated")
    assert key.value == "rotated"


def test_a_secret_whose_value_vanished_fails_the_read_rather_than_read_empty(monkeypatch):
    monkeypatch.setenv("SIGNING_KEY", "first")

    class Env(ocel.Env):
        signing_key: ocel.Secret

    key = Env().signing_key
    monkeypatch.setenv("SIGNING_KEY", "")
    assert key.value == ""
    monkeypatch.delenv("SIGNING_KEY")
    with pytest.raises(ocel.EnvValueError) as raised:
        _ = key.value
    assert raised.value.key == "SIGNING_KEY"
    assert "has no value" in str(raised.value)


def test_a_secret_nothing_is_set_for_fails_the_first_read(monkeypatch):
    monkeypatch.delenv("SIGNING_KEY", raising=False)

    class Env(ocel.Env):
        signing_key: ocel.Secret

    with pytest.raises(ocel.EnvValueError) as raised:
        _ = Env().signing_key
    assert raised.value.key == "SIGNING_KEY"


def test_a_secret_prints_a_redaction_however_it_is_printed(monkeypatch, caplog):
    monkeypatch.setenv("SIGNING_KEY", "s3cret")

    class Env(ocel.Env):
        signing_key: ocel.Secret

    key = Env().signing_key
    percent = "%s"
    printed = [
        str(key),
        repr(key),
        f"{key}",
        f"{key!r}",
        f"{key:>40}",
        format(key),
        percent % key,
        str([key]),
        str({"key": key}),
    ]
    for redacted in printed:
        assert "s3cret" not in redacted
        assert "Secret(SIGNING_KEY)" in redacted

    with caplog.at_level(logging.INFO):
        logging.getLogger("test").info("signing key %s", key)
    assert "s3cret" not in caplog.text
    assert "Secret(SIGNING_KEY)" in caplog.text


def test_two_secrets_under_one_key_are_the_same_secret():
    assert ocel.Secret("K") == ocel.Secret("K")
    assert ocel.Secret("K") != ocel.Secret("OTHER")
    assert len({ocel.Secret("K"), ocel.Secret("K")}) == 1


def test_the_deployment_url_reads_what_ocel_wrote(monkeypatch):
    monkeypatch.setenv("OCEL_URL", "https://web-j-1.ocel.site")
    assert ocel.deployment_url() == "https://web-j-1.ocel.site"

    monkeypatch.setenv("OCEL_VAR_OCEL_URL", "https://baked.ocel.site")
    assert ocel.deployment_url() == "https://baked.ocel.site"


def test_the_deployment_url_says_what_delivers_one_when_none_was(monkeypatch):
    monkeypatch.delenv("OCEL_URL", raising=False)
    monkeypatch.delenv("OCEL_VAR_OCEL_URL", raising=False)

    with pytest.raises(ocel.EnvValueError) as raised:
        ocel.deployment_url()
    assert raised.value.key == "OCEL_URL"
    assert "was not delivered to this app" in str(raised.value)


def test_a_declaration_the_server_refuses_says_what_it_said(monkeypatch):
    monkeypatch.setenv("OCEL_PHASE", "discovery")
    monkeypatch.setenv("OCEL_DEV_SERVER", "http://127.0.0.1:1")

    with pytest.raises(RuntimeError) as raised:

        class Env(ocel.Env):
            name: str

        _ = Env
    assert str(raised.value).startswith("ocel: declare env: ")


def test_an_instance_taken_during_discovery_reads_the_environment_it_stands_in(collector):
    os.environ["OCEL_VAR_GREETING"] = "hello"
    try:

        class Env(ocel.Env):
            greeting: str

        assert Env().greeting == "hello"
    finally:
        del os.environ["OCEL_VAR_GREETING"]

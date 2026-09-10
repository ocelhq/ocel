import inspect
import os
import re
import threading
import types
import typing
import unicodedata
from collections.abc import Sequence
from typing import Any, overload

from ocel._declare import declare_env, discovering, report_env_problems
from ocel.gen.app.resources.v1.variables_pb import (
    DeclareEnvRequest,
    GroupDefinition,
    ReportEnvProblemsRequest,
    VariableCell,
    VariableClass,
    VariableDefinition,
    VariableProblem,
)

__all__ = [
    "Env",
    "EnvDefinitionError",
    "EnvScopeError",
    "EnvValueError",
    "Group",
    "Secret",
    "deployment_url",
    "group",
    "var",
]

DELIVERED_PREFIX = "OCEL_VAR_"
APP_FOLDER_ENV = "OCEL_APP_FOLDER"
RESERVED_PREFIX = "OCEL_"
URL_KEY = "OCEL_URL"

_KEY_PATTERN = re.compile(r"^[A-Z_][A-Z0-9_]*$")
_TRUE = frozenset({"1", "t", "T", "TRUE", "true", "True"})
_FALSE = frozenset({"0", "f", "F", "FALSE", "false", "False"})
_BOOLEANS = "1 t T TRUE true True 0 f F FALSE false False"

_CLASS_NAMES = {
    VariableClass.PLAIN: "plain",
    VariableClass.SENSITIVE: "sensitive",
    VariableClass.SECRET: "secret",
}

_owner_lock = threading.Lock()
_owner: dict[str, "_Site"] = {}
_MEMBERS: dict[type, tuple["_Variable", ...]] = {}

_DECLARED_TWICE = (
    "is declared by two attributes of the same class. A key is declared by exactly one attribute."
)


class EnvDefinitionError(Exception):
    """Raised at class creation when a class of :class:`Env` declares a variable Ocel
    cannot accept: an unusable key, a class that contradicts its default, an annotation
    nothing can parse a value into."""

    key: str
    detail: str

    def __init__(self, key: str, detail: str) -> None:
        super().__init__(f"'{key}' {detail}" if key else detail)
        self.key = key
        self.detail = detail


class EnvValueError(Exception):
    """Raised when a declared variable has no value, or one its annotation rejects, and
    when no deployment url was delivered."""

    key: str
    detail: str

    def __init__(self, key: str, detail: str) -> None:
        super().__init__(f"'{key}' {detail}")
        self.key = key
        self.detail = detail


class EnvScopeError(Exception):
    """Raised when a variable is scoped to folders this app is not bound to."""

    key: str
    folders: tuple[str, ...]
    binding: str

    def __init__(self, key: str, folders: Sequence[str], binding: str) -> None:
        bound = binding or "the project root"
        super().__init__(
            f"'{key}' is scoped to {', '.join(folders)}, but this app is bound to {bound}. "
            f"Bind this app to one of those folders in ocel.json, or widen the variable's scope."
        )
        self.key = key
        self.folders = tuple(folders)
        self.binding = binding


class Secret:
    """A live variable: one whose value can be rotated underneath the running process.
    An attribute of this type declares the variable under the secret class, and
    :attr:`value` resolves the value on every read rather than once at first use, so a
    rotation reaches the next read. A Secret prints a redaction, never the value."""

    __slots__ = ("_key",)

    def __init__(self, key: str) -> None:
        self._key = key

    @property
    def key(self) -> str:
        """The variable the secret was declared under."""
        return self._key

    @property
    def value(self) -> str:
        """The secret's current value. It raises an :class:`EnvValueError` when no value
        stands for the key any more, the way the read of a missing variable fails, rather
        than hand back an empty secret."""
        delivered = _delivered(self._key)
        if delivered is None:
            raise _unset(self._key)
        return delivered

    def __str__(self) -> str:
        return f"Secret({self._key})"

    def __repr__(self) -> str:
        return str(self)

    def __format__(self, _spec: str) -> str:
        return str(self)

    def __eq__(self, other: object) -> bool:
        if not isinstance(other, Secret):
            return NotImplemented
        return self._key == other._key

    def __hash__(self) -> int:
        return hash((Secret, self._key))


class _Unset:
    __slots__ = ()

    def __repr__(self) -> str:
        return "<unset>"


_UNSET = _Unset()


class _Marker:
    __slots__ = ("key", "default", "sensitive", "folders", "description")

    def __init__(
        self,
        key: str | None,
        default: Any,
        sensitive: bool,
        folders: Sequence[str] | None,
        description: str | None,
    ) -> None:
        self.key = key
        self.default = default
        self.sensitive = sensitive
        self.folders = folders
        self.description = description


@overload
def var[T](
    *,
    key: str | None = None,
    default: T,
    sensitive: bool = False,
    folders: Sequence[str] | None = None,
    description: str | None = None,
) -> T: ...


@overload
def var(
    *,
    key: str | None = None,
    sensitive: bool = False,
    folders: Sequence[str] | None = None,
    description: str | None = None,
) -> Any: ...


def var(
    *,
    key: str | None = None,
    default: Any = _UNSET,
    sensitive: bool = False,
    folders: Sequence[str] | None = None,
    description: str | None = None,
) -> Any:
    """Spell out what an attribute of a class of :class:`Env` declares: the key when it is
    not the attribute name upper-cased, the value to fall back on when none is set, the
    sensitive class, and the folders the variable holds a value in."""
    return _Marker(key, default, sensitive, folders, description)


class _Variable:
    __slots__ = (
        "attr",
        "key",
        "klass",
        "target",
        "optional",
        "folders",
        "default_raw",
        "default_value",
        "description",
    )

    def __init__(
        self,
        attr: str,
        key: str,
        klass: VariableClass,
        target: Any,
        optional: bool,
        folders: tuple[str, ...],
        default_raw: str | None,
        default_value: Any,
        description: str,
    ) -> None:
        self.attr = attr
        self.key = key
        self.klass = klass
        self.target = target
        self.optional = optional
        self.folders = folders
        self.default_raw = default_raw
        self.default_value = default_value
        self.description = description

    @property
    def live(self) -> bool:
        return self.target is Secret

    @property
    def required(self) -> bool:
        return not self.defaulted and not self.optional

    @property
    def defaulted(self) -> bool:
        return self.default_raw is not None or self.default_value is not _UNSET

    @property
    def has_schema(self) -> bool:
        return self.target is not str and not self.live

    def complaint(self, message: str) -> str:
        if self.klass is VariableClass.PLAIN:
            return message
        return (
            f"withheld, because a '{_CLASS_NAMES[self.klass]}' value's parse message "
            f"can quote the value itself"
        )

    def __get__(self, instance: object, owner: type | None = None) -> Any:
        if instance is None:
            return self
        value = self.read()
        if not self.live:
            instance.__dict__[self.attr] = value
        return value

    def read(self) -> Any:
        if self.folders and os.environ.get(APP_FOLDER_ENV, "") not in self.folders:
            raise EnvScopeError(self.key, self.folders, os.environ.get(APP_FOLDER_ENV, ""))
        raw = _delivered(self.key)
        if self.live:
            if raw is None:
                raise _unset(self.key)
            return Secret(self.key)
        if raw is None:
            if self.default_value is not _UNSET:
                return self.default_value
            if self.default_raw is not None:
                raw = self.default_raw
            elif self.optional:
                return None
            else:
                raise _unset(self.key)
        try:
            return _parse(self.target, raw)
        except Exception as error:
            raise EnvValueError(
                self.key,
                f"is set but does not satisfy its type: {self.complaint(_said(error))}. "
                f"Fix it with `ocel env set {self.key}=<VALUE>`.",
            ) from None


class _GroupMarker:
    __slots__ = ("key", "description")

    def __init__(self, key: str | None, description: str) -> None:
        self.key = key
        self.description = description


def group(*, key: str | None = None, description: str = "") -> Any:
    """Spell out what an attribute annotated with a class of :class:`Group` declares: the
    name the group is known by when it is not the attribute name, and what turning the
    group on does."""
    return _GroupMarker(key, description)


class Group:
    """A set of variables an app takes together. An attribute of a class of :class:`Env`
    annotated with a class of this one declares the group, and ``T | None`` makes the group
    optional: an optional group reads as ``None`` and is owed nothing until a value stands
    for one of its members.

        class GitHub(ocel.Group):
            client_id: str = ocel.var(key="GITHUB_CLIENT_ID")
            client_secret: ocel.Secret = ocel.var(key="GITHUB_CLIENT_SECRET")

        class Env(ocel.Env):
            github: GitHub | None = None
            smtp: Smtp = ocel.group(description="Send mail")

    A member is reached through the group and nowhere else, and carries its own optional
    spelling: a member annotated ``T | None`` or carrying a default is not owed while the
    group is on. A group holds variables and nothing else, so a group annotated inside one
    is refused where it is written. The class itself declares nothing; the class of
    :class:`Env` that names it declares its members."""

    def __init_subclass__(cls, **kwargs: Any) -> None:
        super().__init_subclass__(**kwargs)
        _refuse_restatement(cls, Group)
        members = _members(cls)
        _MEMBERS[cls] = members
        for member in members:
            setattr(cls, member.attr, member)


class Env:
    """The base class an app's variables are declared on. Subclass it in a file under the
    project's discovery folder, the way :func:`ocel.postgres` is called: the class statement is
    the declaration, and every annotated attribute is a variable read from the environment
    the deploy delivered, on the first access through an instance.

        class Env(ocel.Env):
            database_name: str
            api_key: str = ocel.var(sensitive=True)
            signing_key: ocel.Secret
            port: int = 3000

    The key is the attribute name upper-cased unless :func:`var` names another. An
    attribute annotated ``T | None`` is optional and reads as ``None`` when nothing is set;
    any other attribute is required unless it carries a default. An attribute annotated
    :class:`Secret` declares the secret class: encrypted at rest, delivered live, and read
    through :attr:`Secret.value` on each use rather than once, since the value can rotate
    underneath the process. An attribute annotated with a class of :class:`Group` is the
    set of variables that group holds, taken together.

    The annotation is the schema: ``str`` passes the text through, ``bool`` takes the set
    Ocel accepts in every language, and any other type is applied to the delivered text.
    A class other than plain keeps the value out of every error, since a parser's message
    can quote it."""

    def __init_subclass__(cls, **kwargs: Any) -> None:
        super().__init_subclass__(**kwargs)
        _refuse_restatement(cls, Env)
        site = _site(cls)
        entries = _entries(cls, site)
        for entry in entries:
            setattr(cls, entry.attr, entry)
        if discovering():
            _declare(entries, site)


def deployment_url() -> str:
    """The absolute url this app is served on, scheme and all: the deployed hostname, or
    the local one under ``ocel dev``. Ocel writes it; nothing declares it."""
    delivered = _delivered(URL_KEY)
    if delivered is None:
        raise EnvValueError(
            URL_KEY,
            "was not delivered to this app. Ocel writes it from the hostname the deploy "
            "serves the app on, and this app is served on none: add one under "
            "`domains.production` on the app, or on the project if this is the first app "
            "it names, and deploy again.",
        )
    return delivered


class _Site:
    __slots__ = ("file", "line", "scope", "cls")

    def __init__(self, file: str, line: int, scope: dict[str, Any], cls: str) -> None:
        self.file = file
        self.line = line
        self.scope = scope
        self.cls = cls

    def __str__(self) -> str:
        return f"{self.file}:{self.line}"

    def rerun_of(self, claimed: "_Site") -> bool:
        return self.file == claimed.file and self.scope is not claimed.scope


def _site(cls: type) -> _Site:
    frame = _caller()
    return _Site(frame.filename, frame.lineno, frame.frame.f_globals, cls.__qualname__)


def _caller() -> inspect.FrameInfo:
    for frame in inspect.stack(0):
        if frame.filename != __file__:
            return frame
    raise RuntimeError("ocel: no frame outside the env module declared the class")


def _refuse_restatement(cls: type, base: type) -> None:
    for held in cls.__mro__[1:]:
        if held is not base and held is not object and issubclass(held, base):
            raise EnvDefinitionError(
                "",
                f"{cls.__name__} extends {held.__name__}, which declares variables of "
                f"its own. A class declares the variables its own attributes name: "
                f"subclass ocel.{base.__name__} directly.",
            )


def _members(cls: type) -> tuple[_Variable, ...]:
    members: list[_Variable] = []
    for attr, annotation in inspect.get_annotations(cls, eval_str=True).items():
        if _group_annotation(annotation)[0] is not None:
            raise EnvDefinitionError(
                "",
                f"{cls.__name__}.{attr} holds a group of its own. A group holds "
                f"variables, and groups nest one level only.",
            )
        member = _definition(cls, attr, annotation)
        if any(seen.key == member.key for seen in members):
            raise EnvDefinitionError(member.key, _DECLARED_TWICE)
        members.append(member)
    if not members:
        raise EnvDefinitionError(
            "",
            f"{cls.__name__} declares no variables. A group holds the variables an app "
            f"takes together, so it holds at least one.",
        )
    return tuple(members)


def _entries(cls: type, site: _Site) -> "list[_Variable | _Group]":
    entries: list[_Variable | _Group] = []
    with _owner_lock:
        for attr, annotation in inspect.get_annotations(cls, eval_str=True).items():
            target, optional = _group_annotation(annotation)
            if target is None:
                entry: _Variable | _Group = _definition(cls, attr, annotation)
            else:
                entry = _group(cls, attr, target, optional)
                if any(held.key == entry.key for held in entries if isinstance(held, _Group)):
                    raise EnvDefinitionError(
                        entry.key,
                        "is declared by two attributes of the same class. A group is "
                        "declared by exactly one attribute.",
                    )
            for variable in _variables(entry):
                if any(seen.key == variable.key for seen in _flattened(entries)):
                    raise EnvDefinitionError(variable.key, _DECLARED_TWICE)
                claimed = _owner.get(variable.key)
                if claimed is not None and not site.rerun_of(claimed):
                    if claimed.file != site.file:
                        raise EnvDefinitionError(
                            variable.key,
                            f"is already declared in {claimed.file}. A key may be "
                            f"defined by exactly one file.",
                        )
                    raise EnvDefinitionError(
                        variable.key,
                        f"is already declared by {claimed.cls} in {claimed.file}. A key "
                        f"is declared by exactly one class.",
                    )
            entries.append(entry)
        for variable in _flattened(entries):
            _owner[variable.key] = site
    return entries


def _variables(entry: "_Variable | _Group") -> tuple[_Variable, ...]:
    return entry.members if isinstance(entry, _Group) else (entry,)


def _flattened(entries: Sequence["_Variable | _Group"]) -> list[_Variable]:
    return [variable for entry in entries for variable in _variables(entry)]


class _Group:
    __slots__ = ("attr", "key", "target", "optional", "description", "members")

    def __init__(
        self,
        attr: str,
        key: str,
        target: type,
        optional: bool,
        description: str,
        members: tuple[_Variable, ...],
    ) -> None:
        self.attr = attr
        self.key = key
        self.target = target
        self.optional = optional
        self.description = description
        self.members = members

    @property
    def on(self) -> bool:
        return not self.optional or any(
            _delivered(member.key) is not None for member in self.members
        )

    def __get__(self, instance: object, owner: type | None = None) -> Any:
        if instance is None:
            return self
        value = self.target() if self.on else None
        instance.__dict__[self.attr] = value
        return value


def _group(cls: type, attr: str, target: type, optional: bool) -> _Group:
    assigned = cls.__dict__.get(attr, _UNSET)
    if optional and assigned is None:
        assigned = _UNSET
    if assigned is not _UNSET and not isinstance(assigned, _GroupMarker):
        raise EnvDefinitionError(
            attr,
            "is a group with a value of its own. A group attribute takes ocel.group(), "
            "or None to leave an optional one unset.",
        )
    marker = assigned if isinstance(assigned, _GroupMarker) else None
    key = (marker.key if marker and marker.key else None) or attr
    if "#" in key or any(unicodedata.category(character) == "Cc" for character in key):
        raise EnvDefinitionError(
            key, "is not a usable group name: a group name is one line and has no '#'."
        )
    description = marker.description if marker else ""
    problem = _description_problem(description)
    if problem:
        raise EnvDefinitionError(key, f"has an unusable description: {problem}")
    members = _MEMBERS[target]
    unshared = _unshared_scopes(members)
    if unshared is not None:
        raise EnvDefinitionError(
            key, f"is read as one, and no folder satisfies every member: {unshared}."
        )
    return _Group(attr, key, target, optional, description, members)


def _unshared_scopes(members: Sequence[_Variable]) -> str | None:
    shared: set[str] | None = None
    scoped: list[str] = []
    for member in members:
        if not member.folders:
            continue
        scoped.append(f"{member.key} ({', '.join(member.folders)})")
        folders = set(member.folders)
        shared = folders if shared is None else shared & folders
    if shared is None or shared:
        return None
    return ", ".join(scoped)


def _group_annotation(annotation: Any) -> tuple[type | None, bool]:
    origin = typing.get_origin(annotation)
    if origin is types.UnionType or origin is typing.Union:
        arguments = typing.get_args(annotation)
        inner = [argument for argument in arguments if argument is not type(None)]
        if len(arguments) == 2 and len(inner) == 1 and _is_group(inner[0]):
            return inner[0], True
        return None, False
    if _is_group(annotation):
        return annotation, False
    return None, False


def _is_group(annotation: Any) -> bool:
    if not isinstance(annotation, type) or annotation is Group:
        return False
    return issubclass(annotation, Group)


def _description_problem(description: str) -> str:
    if len(description.encode()) > 120:
        return "a description is at most 120 bytes."
    if any(unicodedata.category(character) == "Cc" for character in description):
        return "a description is one line and has no control characters."
    return ""


def _definition(cls: type, attr: str, annotation: Any) -> _Variable:
    assigned = cls.__dict__.get(attr, _UNSET)
    marker = (
        assigned if isinstance(assigned, _Marker) else _Marker(None, assigned, False, None, None)
    )

    key = marker.key or attr.upper()
    if not _KEY_PATTERN.fullmatch(key):
        raise EnvDefinitionError(
            key,
            "is not a usable variable name: use upper-case letters, digits and "
            "underscores, starting with a letter or underscore.",
        )

    description = marker.description or ""
    problem = _description_problem(description)
    if problem:
        raise EnvDefinitionError(key, f"has an unusable description: {problem}")

    target, optional = _target(key, annotation)
    live = target is Secret
    klass = VariableClass.PLAIN
    if live:
        klass = VariableClass.SECRET
    elif marker.sensitive:
        klass = VariableClass.SENSITIVE

    if live and marker.sensitive:
        raise EnvDefinitionError(
            key,
            "is an ocel.Secret declared sensitive. A Secret attribute is always the "
            "secret class; drop the option.",
        )
    if key == URL_KEY:
        raise EnvDefinitionError(
            key,
            "is written by Ocel for every app, from the hostname the deploy serves it on, "
            "so a declared one would be overwritten before anything read it. Read it with "
            "`ocel.deployment_url()`.",
        )
    if klass is VariableClass.PLAIN and key.startswith(RESERVED_PREFIX):
        raise EnvDefinitionError(
            key,
            f"starts with the reserved prefix {RESERVED_PREFIX}. A "
            f"'{_CLASS_NAMES[klass]}' variable is delivered under its own name, so Ocel "
            f"would overwrite it.",
        )
    if live and marker.default is not _UNSET:
        raise EnvDefinitionError(
            key,
            "is a Secret with a default. A live value must fail loudly when it is missing "
            "rather than fall back.",
        )
    if optional and target is Secret:
        raise EnvDefinitionError(
            key,
            "is an optional Secret. A live value must fail loudly when it is missing "
            "rather than fall back; declare it as an ocel.Secret.",
        )

    folders: tuple[str, ...] = ()
    if marker.folders is not None:
        folders = tuple(marker.folders)
        problem = _scope_problem(folders)
        if problem:
            raise EnvDefinitionError(key, f"has an unusable folder scope: {problem}")

    variable = _Variable(attr, key, klass, target, optional, folders, None, _UNSET, description)
    if marker.default is not _UNSET and not (optional and marker.default is None):
        _default(variable, marker.default)
    return variable


def _default(variable: _Variable, default: Any) -> None:
    if isinstance(default, str) and variable.target is not str:
        try:
            _parse(variable.target, default)
        except Exception as error:
            raise EnvDefinitionError(
                variable.key,
                f"has a default its own type rejects: {variable.complaint(_said(error))}.",
            ) from None
        variable.default_raw = default
        return
    if not isinstance(default, variable.target):
        raise EnvDefinitionError(
            variable.key,
            f"has a default its own type rejects: a {type(default).__name__} where the "
            f"annotation says {_render(variable.target)}.",
        )
    variable.default_value = default


def _target(key: str, annotation: Any) -> tuple[Any, bool]:
    origin = typing.get_origin(annotation)
    if origin is types.UnionType or origin is typing.Union:
        arguments = typing.get_args(annotation)
        inner = [argument for argument in arguments if argument is not type(None)]
        if len(arguments) != 2 or len(inner) != 1:
            raise _unparseable(key, annotation)
        target, _ = _target(key, inner[0])
        return target, True
    if annotation is Secret or annotation is str or annotation is bool:
        return annotation, False
    if isinstance(annotation, type) and issubclass(annotation, (Env, Group)):
        raise EnvDefinitionError(
            key,
            f"is read into {annotation.__name__}, which declares variables of its own. "
            f"The variables an app takes together are a class of ocel.Group, and the "
            f"attribute annotated with it is the group.",
        )
    if isinstance(annotation, type):
        return annotation, False
    raise _unparseable(key, annotation)


def _unparseable(key: str, annotation: Any) -> EnvDefinitionError:
    return EnvDefinitionError(
        key,
        f"is read into a {_render(annotation)}, which nothing parses a value into. Use a "
        f"str, a bool, an ocel.Secret, any type whose constructor takes the delivered "
        f"text, or 'T | None' for an optional one.",
    )


def _render(annotation: Any) -> str:
    if isinstance(annotation, type):
        return annotation.__name__
    return str(annotation)


def _scope_problem(folders: Sequence[str]) -> str:
    if not folders:
        return (
            "an empty folder scope says nothing. Leave 'folders' off to keep the variable "
            "at the project root."
        )
    seen: set[str] = set()
    for folder in folders:
        if folder in seen:
            return (
                f"folder '{folder}' is named twice. A scoped variable holds one value per "
                f"folder it names."
            )
        seen.add(folder)
        problem = _folder_problem(folder)
        if problem:
            return f"folder '{folder}': {problem}"
    return ""


def _folder_problem(folder: str) -> str:
    if not folder.startswith("/"):
        return "a folder path must start with '/'."
    if folder == "/":
        return (
            "'/' is the project root, which is what an unscoped variable already uses. "
            "Leave 'folders' off instead."
        )
    if folder.endswith("/"):
        return "a folder path must not end with '/'."
    if "//" in folder:
        return "a folder path has no empty segments."
    if "#" in folder:
        return "a folder path may not contain '#'."
    return ""


def _declare(entries: Sequence[_Variable | _Group], site: _Site) -> None:
    source = str(site)
    definitions: list[VariableDefinition] = []
    groups: list[GroupDefinition] = []
    for entry in entries:
        held = entry.key if isinstance(entry, _Group) else ""
        if isinstance(entry, _Group):
            groups.append(
                GroupDefinition(
                    key=entry.key, required=not entry.optional, description=entry.description
                )
            )
        for variable in _variables(entry):
            definitions.append(
                VariableDefinition(
                    key=variable.key,
                    class_=variable.klass,
                    required=variable.required,
                    folders=list(variable.folders),
                    source=source,
                    has_schema=variable.has_schema,
                    description=variable.description,
                    group=held,
                )
            )
    response = declare_env(DeclareEnvRequest(definitions=definitions, groups=groups))
    problems = _problems(entries, response.cells)
    if problems:
        report_env_problems(ReportEnvProblemsRequest(problems=problems))


def _problems(
    entries: Sequence[_Variable | _Group], cells: Sequence[VariableCell]
) -> list[VariableProblem]:
    problems: list[VariableProblem] = []
    for entry in entries:
        gate = entry if isinstance(entry, _Group) and entry.optional else None
        for variable in _variables(entry):
            problems.extend(_faults(variable, cells, gate))
    return problems


def _held(variable: _Variable, cells: Sequence[VariableCell], folder: str) -> bool:
    def at(where: str) -> bool:
        return any(cell.key == variable.key and cell.folder == where for cell in cells)

    if variable.folders:
        return folder != "" and folder in variable.folders and at(folder)
    return (folder != "" and at(folder)) or at("")


def _switched_on(group: _Group, cells: Sequence[VariableCell], folder: str) -> bool:
    return any(_held(member, cells, folder) for member in group.members)


def _faults(
    variable: _Variable, cells: Sequence[VariableCell], gate: "_Group | None"
) -> list[VariableProblem]:
    problems: list[VariableProblem] = []
    stored = [cell for cell in cells if cell.key == variable.key]
    if variable.required:
        for folder in variable.folders or ("",):
            if gate is not None and not _switched_on(gate, cells, folder):
                continue
            if not any(cell.folder == folder for cell in stored):
                problems.append(
                    VariableProblem(
                        key=variable.key, folder=folder, kind=VariableProblem.Kind.MISSING
                    )
                )
    if not variable.has_schema:
        return problems
    for cell in stored:
        try:
            _parse(variable.target, cell.value)
        except Exception as error:
            problems.append(
                VariableProblem(
                    key=variable.key,
                    folder=cell.folder,
                    kind=VariableProblem.Kind.INVALID,
                    detail=variable.complaint(_said(error)),
                )
            )
    return problems


def _parse(target: Any, raw: str) -> Any:
    if target is str:
        return raw
    if target is bool:
        if raw in _TRUE:
            return True
        if raw in _FALSE:
            return False
        raise ValueError(f"want one of {_BOOLEANS}")
    return target(raw)


def _said(error: Exception) -> str:
    return str(error).strip() or type(error).__name__


def _delivered(key: str) -> str | None:
    delivered = os.environ.get(DELIVERED_PREFIX + key)
    if delivered is not None:
        return delivered
    return os.environ.get(key)


def _unset(key: str) -> EnvValueError:
    return EnvValueError(key, f"has no value. Set one with `ocel env set {key}=<VALUE>`.")

import asyncio
import inspect
import ssl
from urllib.parse import quote

from protobuf import Oneof

from ocel._binding import postgres_binding, unprovisioned
from ocel._declare import declare, discovering
from ocel.gen.app.resources.v1.resources_pb import (
    DeclareRequest,
    PostgresConfig,
    ResourceIdentifier,
    ResourceType,
)
from ocel.gen.common.bindings.v1.bindings_pb import PostgresTlsMode

_KIND = "postgres"
_DEFAULT_VERSION = "17"
_SSLMODES = {PostgresTlsMode.REQUIRE: "require", PostgresTlsMode.VERIFY_FULL: "verify-full"}


class Postgres:
    """A postgres database an app declares and reads its binding from."""

    #: The name the database was declared under, and the name its binding is delivered as.
    name: str

    def __init__(self, name: str):
        """Take the handle for the database named ``name``. Prefer :func:`postgres`,
        which declares the database as well as handing back its handle."""
        self.name = name
        self._pool = None
        self._opening = asyncio.Lock()

    @property
    def connection_string(self) -> str:
        """The postgres URL of the delivered binding: the record's url verbatim when it
        carries one, and otherwise one built from its host, port, database and credentials,
        percent-encoded, with its tls mode as ``sslmode``."""
        properties = postgres_binding(self.name)
        if properties.url:
            return properties.url
        user = quote(properties.username, safe="")
        password = quote(properties.password, safe="")
        database = quote(properties.database, safe="")
        sslmode = _SSLMODES.get(properties.tls_mode)
        query = f"?sslmode={sslmode}" if sslmode else ""
        return f"postgres://{user}:{password}@{properties.host}:{properties.port}/{database}{query}"

    async def pool(self):
        """The asyncpg pool over the delivered binding, opened on the first call and returned
        as it stands on every one after. A record under verify-full that names a CA trusts
        that CA for the server's certificate."""
        if self._pool is None:
            async with self._opening:
                if self._pool is None:
                    import asyncpg

                    options = {}
                    ca = postgres_binding(self.name).tls_ca
                    if ca:
                        context = ssl.create_default_context()
                        context.load_verify_locations(cadata=ca)
                        options["ssl"] = context
                    self._pool = await asyncpg.create_pool(self.connection_string, **options)
        return self._pool

    async def fetch(self, query: str, *args):
        """Run a query on the pool and return the rows it selected."""
        pool = await self.pool()
        return await pool.fetch(query, *args)


class _Unprovisioned(Postgres):
    def __getattribute__(self, access: str):
        if access == "name" or (access.startswith("__") and access.endswith("__")):
            return object.__getattribute__(self, access)
        raise unprovisioned(f'postgres("{object.__getattribute__(self, "name")}")', access)


def postgres(name: str, *, version: str | None = None) -> Postgres:
    """Declare a postgres database named ``name`` and return the handle an app reads it
    through. Call it from a file under the project's discovery folder: during discovery the
    call is the declaration, and at runtime it reads the binding the deploy delivered for
    that name."""
    if not discovering():
        return Postgres(name)
    caller = inspect.stack(0)[1]
    declare(
        DeclareRequest(
            resource=ResourceIdentifier(type=ResourceType.POSTGRES, name=name),
            config=Oneof(_KIND, PostgresConfig(version=version or _DEFAULT_VERSION)),
            source=f"{caller.filename}:{caller.lineno}",
        )
    )
    return _Unprovisioned(name)

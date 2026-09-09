import asyncio
import inspect
from urllib.parse import quote

from protobuf import Oneof

from ocel._declare import declare, discovering
from ocel._link import link, unprovisioned
from ocel.gen.app.resources.v1.resources_pb import (
    DeclareRequest,
    PostgresConfig,
    ResourceIdentifier,
)
from ocel.gen.common.links.v1.links_pb import LinkType

_KIND = "postgres"
_DEFAULT_VERSION = "17"


class Postgres:
    """A postgres database an app declares and reads its link from."""

    #: The name the database was declared under, and the name its link is delivered as.
    name: str

    def __init__(self, name: str):
        """Take the handle for the database named ``name``. Prefer :func:`postgres`,
        which declares the database as well as handing back its handle."""
        self.name = name
        self._pool = None
        self._opening = asyncio.Lock()

    @property
    def connection_string(self) -> str:
        """The postgres URL of the delivered link, with the credentials percent-encoded."""
        properties = link(self.name)
        user = quote(properties.username, safe="")
        password = quote(properties.password, safe="")
        database = quote(properties.database, safe="")
        return f"postgres://{user}:{password}@{properties.host}:{properties.port}/{database}"

    async def pool(self):
        """The asyncpg pool over the delivered link, opened on the first call and returned
        as it stands on every one after."""
        if self._pool is None:
            async with self._opening:
                if self._pool is None:
                    import asyncpg

                    self._pool = await asyncpg.create_pool(self.connection_string)
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
    call is the declaration, and at runtime it reads the link the deploy delivered for
    that name."""
    if not discovering():
        return Postgres(name)
    caller = inspect.stack(0)[1]
    declare(
        DeclareRequest(
            resource=ResourceIdentifier(type=LinkType.POSTGRES, name=name),
            config=Oneof(_KIND, PostgresConfig(version=version or _DEFAULT_VERSION)),
            source=f"{caller.filename}:{caller.lineno}",
        )
    )
    return _Unprovisioned(name)

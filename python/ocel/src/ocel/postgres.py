import inspect
from urllib.parse import quote

from ocel._declare import declare, discovering
from ocel._link import link, unprovisioned

_KIND = "postgres"
_DEFAULT_VERSION = "17"


class Postgres:
    """A postgres database an app declares and reads its link from."""

    def __init__(self, name: str):
        self.name = name
        self._pool = None

    @property
    def connection_string(self) -> str:
        """The postgres URL of the delivered link, with the credentials percent-encoded."""
        properties = link(self.name, _KIND)
        user = quote(properties.get("username", ""), safe="")
        password = quote(properties.get("password", ""), safe="")
        host = properties.get("host", "")
        port = properties.get("port", "")
        database = quote(properties.get("database", ""), safe="")
        return f"postgres://{user}:{password}@{host}:{port}/{database}"

    async def pool(self):
        """The asyncpg pool over the delivered link, opened on the first call and returned
        as it stands on every one after."""
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
        if access == "name":
            return object.__getattribute__(self, access)
        raise unprovisioned(f'postgres("{object.__getattribute__(self, "name")}")', access)


def postgres(name: str, *, version: str | None = None) -> Postgres:
    """Declare a postgres database named ``name`` and return the handle an app reads it
    through. Call it from a file under the project's infra folder: during discovery the
    call is the declaration, and at runtime it reads the link the deploy delivered for
    that name."""
    if not discovering():
        return Postgres(name)
    caller = inspect.stack(0)[1]
    declare(
        _KIND,
        name,
        {"version": version or _DEFAULT_VERSION},
        f"{caller.filename}:{caller.lineno}",
    )
    return _Unprovisioned(name)

from __future__ import annotations

import ssl
from collections.abc import Mapping
from typing import Any

import redis.asyncio.connection
import redis.connection


class AuthorityConnection(redis.connection.Connection):
    def __init__(self, *, ssl_authority: ssl.SSLContext, ssl_server_name: str = "", **kwargs: Any):
        self._authority = ssl_authority
        self._server_name = ssl_server_name
        super().__init__(**kwargs)

    def _connect(self):
        plain = super()._connect()
        try:
            return self._authority.wrap_socket(
                plain, server_hostname=self._server_name or self.host
            )
        except OSError:
            plain.close()
            raise


class AsyncAuthorityConnection(redis.asyncio.connection.Connection):
    def __init__(self, *, ssl_authority: ssl.SSLContext, ssl_server_name: str = "", **kwargs: Any):
        self._authority = ssl_authority
        self._server_name = ssl_server_name
        super().__init__(**kwargs)

    def _connection_arguments(self) -> Mapping:
        return {
            **super()._connection_arguments(),
            "ssl": self._authority,
            "server_hostname": self._server_name or self.host,
        }

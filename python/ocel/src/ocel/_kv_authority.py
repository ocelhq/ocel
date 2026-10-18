from __future__ import annotations

import socket
import ssl
from collections.abc import Mapping
from typing import Any, cast

import redis.asyncio.connection
import redis.connection


class AuthorityConnection(redis.connection.Connection):
    def __init__(self, *, ssl_authority: ssl.SSLContext, ssl_server_name: str = "", **kwargs: Any):
        self._authority = ssl_authority
        self._server_name = ssl_server_name
        cast(Any, super()).__init__(**kwargs)

    def _connect(self) -> socket.socket:
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
        cast(Any, super()).__init__(**kwargs)

    def _connection_arguments(self) -> Mapping[str, object]:
        inherited = cast(Mapping[str, object], cast(Any, super())._connection_arguments())
        return {
            **inherited,
            "ssl": self._authority,
            "server_hostname": self._server_name or self.host,
        }

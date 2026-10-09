from __future__ import annotations

import socket
import threading
import time
import traceback
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from email.utils import format_datetime
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import ddp.http as http
from ddp.http import HTTPError, RedirectError, ResponseTooLarge, request


class _Server:
    def __init__(self, handler: type[BaseHTTPRequestHandler]) -> None:
        self.server = HTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def __enter__(self) -> str:
        self.thread.start()
        return f"http://127.0.0.1:{self.server.server_port}"

    def __exit__(self, *_: object) -> None:
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()


def _handler(fn: Callable[[BaseHTTPRequestHandler], None]) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            fn(self)

        def do_POST(self) -> None:
            fn(self)

        def log_message(self, format: str, *args: object) -> None:
            pass

    return Handler


def test_timeout_and_response_size_are_enforced_by_real_server() -> None:
    def slow(h: BaseHTTPRequestHandler) -> None:
        time.sleep(0.1)
        h.send_response(200)
        h.end_headers()

    with _Server(_handler(slow)) as url, pytest.raises(HTTPError) as error:
        request("GET", url, timeout=0.01, max_retries=0)
    assert str(error.value) == "HTTP request failed"

    def large(h: BaseHTTPRequestHandler) -> None:
        h.send_response(200)
        h.end_headers()
        h.wfile.write(b"1234")

    with _Server(_handler(large)) as url, pytest.raises(ResponseTooLarge):
        request("GET", url, timeout=1, max_response_bytes=3, max_retries=0)


@pytest.mark.parametrize(
    ("kwargs", "secret"),
    [
        ({"method": "GET\nX-Secret: method-secret", "url": "http://example.test"}, "method-secret"),
        ({"method": "GET", "url": "http://example.test/\nurl-secret"}, "url-secret"),
        (
            {"method": "GET", "url": "http://example.test", "headers": {"X": "\nhead-secret"}},
            "head-secret",
        ),
        (
            {"method": "GET", "url": "http://example.test", "auth_header": (1, "auth-secret")},
            "auth-secret",
        ),
        ({"method": "GET", "url": "http://example.test", "body": "body-secret"}, "body-secret"),
    ],
)
def test_invalid_request_data_never_leaks(kwargs: dict[str, object], secret: str) -> None:
    with pytest.raises(ValueError) as error:
        request(timeout=1, max_retries=0, **kwargs)  # pyright: ignore[reportArgumentType]
    assert secret not in str(error.value)


def test_auth_header_is_delivered_and_server_errors_are_safe() -> None:
    def respond(h: BaseHTTPRequestHandler) -> None:
        assert h.headers["X-API-Key"] == "secret-value"
        h.send_response(401)
        h.end_headers()
        h.wfile.write(b"response secret")

    with _Server(_handler(respond)) as url, pytest.raises(HTTPError) as error:
        request(
            "GET",
            f"{url}/?token=query-secret",
            timeout=1,
            headers={"X-API-Key": "old1", "x-api-key": "old2"},
            auth_header=("X-API-Key", "secret-value"),
            max_retries=0,
        )
    assert str(error.value) == "HTTP request failed"
    assert error.value.status == 401


def test_connection_drop_retries_get_but_not_post(monkeypatch: pytest.MonkeyPatch) -> None:
    calls = 0

    def drop(h: BaseHTTPRequestHandler) -> None:
        nonlocal calls
        calls += 1
        h.connection.shutdown(socket.SHUT_RDWR)
        h.connection.close()

    def no_sleep(_: float) -> None:
        pass

    monkeypatch.setattr(http.time, "sleep", no_sleep)
    with _Server(_handler(drop)) as url, pytest.raises(HTTPError):
        request("GET", url, timeout=1, max_retries=1)
    assert calls == 2

    calls = 0
    with _Server(_handler(drop)) as url, pytest.raises(HTTPError):
        request("POST", url, body=b"write", timeout=1, max_retries=3)
    assert calls == 1


def test_retry_after_is_case_insensitive_and_observed(monkeypatch: pytest.MonkeyPatch) -> None:
    calls = 0
    sleeps: list[float] = []

    def respond(h: BaseHTTPRequestHandler) -> None:
        nonlocal calls
        calls += 1
        if calls == 1:
            h.send_response(429)
            h.send_header("retry-after", "2")
            h.end_headers()
            return
        h.send_response(200)
        h.end_headers()
        h.wfile.write(b"ok")

    monkeypatch.setattr(http.time, "sleep", sleeps.append)
    with _Server(_handler(respond)) as url:
        result = request("GET", url, timeout=1, max_retries=1)
    assert result.body == b"ok"
    assert calls == 2
    assert sleeps == [2]


def test_retry_after_date_and_excessive_wait(monkeypatch: pytest.MonkeyPatch) -> None:
    sleeps: list[float] = []
    future = format_datetime(datetime.now(UTC) + timedelta(seconds=2), usegmt=True)
    responses = iter([(503, future), (200, None)])

    def respond(h: BaseHTTPRequestHandler) -> None:
        status, retry_after = next(responses)
        h.send_response(status)
        if retry_after:
            h.send_header("Retry-After", retry_after)
        h.end_headers()

    monkeypatch.setattr(http.time, "sleep", sleeps.append)
    with _Server(_handler(respond)) as url:
        request("GET", url, timeout=1, max_retries=1)
    assert len(sleeps) == 1
    assert 0 <= sleeps[0] <= 2

    calls = 0

    def too_long(h: BaseHTTPRequestHandler) -> None:
        nonlocal calls
        calls += 1
        h.send_response(429, "status-secret")
        h.send_header("Retry-After", "60")
        h.end_headers()

    sleeps.clear()
    with _Server(_handler(too_long)) as url, pytest.raises(HTTPError) as error:
        request("GET", url, timeout=1, max_retries=3)
    assert calls == 1
    assert sleeps == []
    assert "status-secret" not in "".join(traceback.format_exception(error.value))


def test_redirect_is_rejected_before_credentials_follow() -> None:
    target_calls = 0

    def redirect(h: BaseHTTPRequestHandler) -> None:
        nonlocal target_calls
        if h.path == "/target":
            target_calls += 1
        h.send_response(302)
        h.send_header("Location", "/target?secret=redirect-secret")
        h.end_headers()

    with _Server(_handler(redirect)) as url, pytest.raises(RedirectError) as error:
        request(
            "GET",
            f"{url}/start",
            timeout=1,
            auth_header=("Authorization", "Bearer secret"),
            max_retries=0,
        )
    assert str(error.value) == "redirect rejected"
    assert target_calls == 0

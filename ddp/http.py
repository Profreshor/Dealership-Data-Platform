"""Small, bounded HTTP primitive for client jobs."""

from __future__ import annotations

import http.client
import math
import re
import time
import urllib.error
import urllib.request
from collections.abc import Mapping
from datetime import UTC, datetime
from email.utils import parsedate_to_datetime
from typing import BinaryIO
from urllib.parse import urlsplit


class HTTPError(RuntimeError):
    """An HTTP failure with no request or response data in its message."""

    def __init__(self, message: str, *, status: int | None = None) -> None:
        super().__init__(message)
        self.status = status


class RedirectError(HTTPError):
    """The server requested a redirect, which this primitive rejects."""


class ResponseTooLarge(HTTPError):
    """The response exceeded the configured byte limit."""


class Response:
    def __init__(self, status: int, headers: Mapping[str, str], body: bytes) -> None:
        self.status = status
        self.headers = dict(headers)
        self.body = body


_RETRYABLE_METHODS = frozenset({"GET", "HEAD", "OPTIONS", "PUT", "DELETE"})
_RETRYABLE_STATUSES = frozenset({429, 500, 502, 503, 504})
_MAX_RETRIES = 3
_MAX_RETRY_WAIT = 30.0
_TOKEN = re.compile(r"^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(  # pyright: ignore[reportIncompatibleMethodOverride]
        self,
        req: urllib.request.Request,
        fp: BinaryIO,
        code: int,
        msg: str,
        headers: Mapping[str, str],  # pyright: ignore[reportIncompatibleMethodOverride]
        newurl: str,
    ) -> None:
        return None


def request(
    method: str,
    url: str,
    *,
    body: bytes | None = None,
    headers: Mapping[str, str] | None = None,
    auth_header: tuple[str, str] | None = None,
    timeout: float,
    max_retries: int = 2,
    max_response_bytes: int = 1_048_576,
) -> Response:
    """Make one bounded request; response parsing remains client code."""
    _validate_limits(timeout, max_retries, max_response_bytes)
    verb, safe_url, request_headers = _validate_request(method, url, body, headers, auth_header)
    opener = urllib.request.build_opener(_NoRedirect())
    retries = max_retries if verb in _RETRYABLE_METHODS else 0
    waited = 0.0

    for attempt in range(retries + 1):
        try:
            req = urllib.request.Request(safe_url, data=body, headers=request_headers, method=verb)
            with opener.open(req, timeout=timeout) as response:
                response_body = _read_bounded(response, max_response_bytes)
                status = response.status
                response_headers = dict(response.headers.items())
        except urllib.error.HTTPError as exc:
            try:
                status = exc.code
                response_headers = dict(exc.headers.items())
            finally:
                exc.close()
            if 300 <= status < 400:
                raise RedirectError("redirect rejected", status=status) from None
            if status not in _RETRYABLE_STATUSES or attempt == retries:
                raise HTTPError("HTTP request failed", status=status) from None
            waited = _wait_before_retry(response_headers, attempt, waited, status)
            continue
        except (
            urllib.error.URLError,
            TimeoutError,
            ConnectionError,
            http.client.HTTPException,
            OSError,
        ):
            if attempt == retries:
                raise HTTPError("HTTP request failed") from None
            waited = _wait_before_retry(None, attempt, waited)
            continue
        except (TypeError, ValueError, UnicodeError):
            raise HTTPError("HTTP request failed") from None

        if status in _RETRYABLE_STATUSES:
            if attempt == retries:
                raise HTTPError("HTTP request failed", status=status)
            waited = _wait_before_retry(response_headers, attempt, waited, status)
            continue
        if status >= 400:
            raise HTTPError("HTTP request failed", status=status)
        return Response(status, response_headers, response_body)

    raise AssertionError("unreachable")


def _validate_limits(timeout: float, max_retries: int, max_response_bytes: int) -> None:
    if type(timeout) not in (int, float) or not math.isfinite(timeout) or timeout <= 0:
        raise ValueError("timeout must be a finite positive number")
    if type(max_retries) is not int or not 0 <= max_retries <= _MAX_RETRIES:
        raise ValueError(f"max_retries must be between 0 and {_MAX_RETRIES}")
    if type(max_response_bytes) is not int or max_response_bytes <= 0:
        raise ValueError("max_response_bytes must be a positive integer")


def _validate_request(
    method: str,
    url: str,
    body: bytes | None,
    headers: Mapping[str, str] | None,
    auth_header: tuple[str, str] | None,
) -> tuple[str, str, dict[str, str]]:
    if type(method) is not str or not _TOKEN.fullmatch(method):
        raise ValueError("method must be a valid HTTP token")
    if type(url) is not str or not url or not url.isascii() or _has_control(url):
        raise ValueError("url must be a safe HTTP or HTTPS URL")
    try:
        parsed = urlsplit(url)
        _ = parsed.port
        valid_url = (
            parsed.scheme in {"http", "https"}
            and parsed.hostname is not None
            and parsed.username is None
            and parsed.password is None
        )
    except ValueError:
        valid_url = False
    if not valid_url:
        raise ValueError("url must be a safe HTTP or HTTPS URL")
    if body is not None and type(body) is not bytes:
        raise ValueError("body must be bytes")
    if headers is not None and not isinstance(  # pyright: ignore[reportUnnecessaryIsInstance]
        headers, Mapping
    ):
        raise ValueError("headers must be a mapping")
    try:
        request_headers = dict(headers or {})
    except Exception:
        raise ValueError("headers must be a safe string mapping") from None
    if any(not _valid_header(name, value) for name, value in request_headers.items()):
        raise ValueError("headers must contain safe string names and values")
    request_headers = {name.lower(): value for name, value in request_headers.items()}
    if auth_header is not None:
        if type(auth_header) is not tuple or len(auth_header) != 2:
            raise ValueError("auth_header must contain a safe name and value")
        name, value = auth_header
        if not _valid_header(name, value) or not value:
            raise ValueError("auth_header must contain a safe name and value")
        request_headers[name.lower()] = value
    return method.upper(), url, request_headers


def _valid_header(name: object, value: object) -> bool:
    return (
        type(name) is str
        and type(value) is str
        and _TOKEN.fullmatch(name) is not None
        and not _has_control(value)
        and value.isascii()
    )


def _has_control(value: str) -> bool:
    return any(ord(char) < 32 or ord(char) == 127 for char in value)


def _read_bounded(stream: BinaryIO, limit: int) -> bytes:
    body = stream.read(limit + 1)
    if len(body) > limit:
        raise ResponseTooLarge("HTTP response exceeded byte limit")
    return body


def _wait_before_retry(
    headers: Mapping[str, str] | None,
    attempt: int,
    waited: float,
    status: int | None = None,
) -> float:
    delay = _retry_after(headers)
    if delay is None:
        delay = float(2**attempt)
    delay = max(delay, 0.0)
    if delay > _MAX_RETRY_WAIT - waited:
        raise HTTPError("HTTP request failed", status=status) from None
    time.sleep(delay)
    return waited + delay


def _retry_after(headers: Mapping[str, str] | None) -> float | None:
    if not headers:
        return None
    value = next((value for name, value in headers.items() if name.lower() == "retry-after"), None)
    if value is None:
        return None
    try:
        seconds = float(value)
        if math.isfinite(seconds):
            return seconds
    except ValueError:
        pass
    try:
        date = parsedate_to_datetime(value)
        if date.tzinfo is None:
            date = date.replace(tzinfo=UTC)
        return (date.astimezone(UTC) - datetime.now(UTC)).total_seconds()
    except (TypeError, ValueError, OverflowError):
        return None

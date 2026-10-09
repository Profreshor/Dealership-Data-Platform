from __future__ import annotations

import json
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import datetime
from hashlib import sha256
from typing import Any, cast
from zoneinfo import ZoneInfo


@dataclass(frozen=True)
class ReadRef:
    ref: str


@dataclass(frozen=True)
class WriteRef:
    target: str
    mode: str
    key: str | list[str] | None = None


@dataclass(frozen=True)
class JobContext:
    execution_id: str
    attempt_id: str
    scheduled_at: datetime
    timezone: str
    reads: tuple[ReadRef, ...]
    writes: tuple[WriteRef, ...]
    settings: Mapping[str, Any]
    integrations: Mapping[str, Mapping[str, Any]]
    watermarks: Mapping[str, Any]

    @classmethod
    def from_dict(cls, value: object) -> JobContext:
        if not isinstance(value, dict):
            raise ValueError("context must be a JSON object")
        value = cast(dict[str, object], value)
        required = {
            "execution_id", "attempt_id", "scheduled_at", "timezone", "reads", "writes",
            "settings", "integrations", "watermarks",
        }
        missing = required - value.keys()
        if missing:
            raise ValueError(f"context missing fields: {', '.join(sorted(missing))}")
        unknown = value.keys() - required
        if unknown:
            raise ValueError(f"context has unknown fields: {', '.join(sorted(unknown))}")
        execution_id = _string(value, "execution_id")
        attempt_id = _string(value, "attempt_id")
        timezone = _string(value, "timezone")
        try:
            ZoneInfo(timezone)
        except Exception as exc:
            raise ValueError("context timezone must be a valid IANA timezone") from exc
        scheduled_at = _parse_time(_string(value, "scheduled_at"))
        reads_value = value["reads"]
        if not isinstance(reads_value, list):
            raise ValueError("context reads must be a list of strings")
        reads_items = cast(list[object], reads_value)
        if not all(isinstance(item, str) for item in reads_items):
            raise ValueError("context reads must be a list of strings")
        reads_value = cast(list[str], reads_items)
        if any(not item.strip() for item in reads_value):
            raise ValueError("context reads must contain non-empty references")
        writes_value = value["writes"]
        if not isinstance(writes_value, list):
            raise ValueError("context writes must be a list")
        writes_value = cast(list[object], writes_value)
        writes = tuple(_write(item) for item in writes_value)
        settings = _mapping(value["settings"], "settings")
        integrations_value = _mapping(value["integrations"], "integrations")
        integrations: dict[str, Mapping[str, Any]] = {}
        for name, config in integrations_value.items():
            integrations[name] = _mapping(config, f"integration {name}")
        watermarks = _mapping(value["watermarks"], "watermarks")
        return cls(
            execution_id, attempt_id, scheduled_at, timezone,
            tuple(ReadRef(item) for item in reads_value), writes, settings,
            integrations, watermarks,
        )

    def integration(self, name: str) -> Mapping[str, Any]:
        try:
            return self.integrations[name]
        except KeyError as exc:
            raise KeyError(f"integration not declared: {name}") from exc

    def watermark(self, name: str, default: Any = None) -> Any:
        return self.watermarks.get(name, default)

    def effect_key(self, name: str) -> str:
        if not name.strip():
            raise ValueError("effect name must be non-empty")
        return sha256(f"{self.execution_id}\x1f{name}".encode()).hexdigest()

    def log(self, event: str, **fields: Any) -> None:
        print(json.dumps({**fields, "event": event, "execution_id": self.execution_id,
                          "attempt_id": self.attempt_id}, allow_nan=False), flush=True)


def _string(value: dict[str, object], key: str) -> str:
    item = value[key]
    if not isinstance(item, str) or not item:
        raise ValueError(f"context {key} must be a non-empty string")
    return item


def _mapping(value: object, name: str) -> Mapping[str, Any]:
    if not isinstance(value, dict):
        raise ValueError(f"context {name} must be an object")
    items = cast(dict[object, object], value)
    if not all(isinstance(key, str) for key in items):
        raise ValueError(f"context {name} must be an object")
    return cast(Mapping[str, Any], value)


def _parse_time(value: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("context scheduled_at must be ISO 8601") from exc
    if parsed.tzinfo is None:
        raise ValueError("context scheduled_at must include a timezone")
    return parsed


def _write(value: object) -> WriteRef:
    if not isinstance(value, dict):
        raise ValueError("each context write needs target and mode")
    value = cast(dict[str, object], value)
    unknown = set(value) - {"target", "mode", "key"}
    if unknown:
        raise ValueError(f"context write has unknown fields: {', '.join(sorted(unknown))}")
    if not isinstance(value.get("target"), str) or not isinstance(value.get("mode"), str):
        raise ValueError("each context write needs target and mode")
    target = cast(str, value["target"])
    mode = cast(str, value["mode"])
    if not target.strip() or "." not in target:
        raise ValueError("context write target must be a schema.table name")
    if mode not in {"upsert", "replace", "append"}:
        raise ValueError("context write mode must be upsert, replace, or append")
    key = value.get("key")
    if key is not None and not isinstance(key, (str, list)):
        raise ValueError("context write key must be a string or list")
    if isinstance(key, list):
        key_items = cast(list[object], key)
        if not all(isinstance(item, str) for item in key_items):
            raise ValueError("context write key list must contain strings")
        key_strings = cast(list[str], key_items)
        if not key_strings or any(not item.strip() for item in key_strings):
            raise ValueError("context write key list must be non-empty")
    if isinstance(key, str) and not key.strip():
        raise ValueError("context write key must be non-empty")
    if mode == "upsert" and key is None:
        raise ValueError("upsert writes require key")
    return WriteRef(target, mode, cast(str | list[str] | None, key))

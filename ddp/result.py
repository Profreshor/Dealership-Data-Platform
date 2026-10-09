from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any, cast


@dataclass(frozen=True)
class JobResult:
    rows_read: int | None = None
    rows_written: int | None = None
    watermark: Any = None
    warnings: tuple[str, ...] = field(default_factory=tuple)
    details: dict[str, Any] = field(default_factory=dict[str, Any])

    def __post_init__(self) -> None:
        for name, value in (("rows_read", self.rows_read), ("rows_written", self.rows_written)):
            value = cast(object, value)
            if value is not None and (
                isinstance(value, bool) or not isinstance(value, int) or value < 0
            ):
                raise ValueError(f"{name} must be a non-negative integer")
        warnings = cast(object, self.warnings)
        if not isinstance(warnings, (tuple, list)):
            raise ValueError("warnings must be a tuple or list of strings")
        warning_items = cast(tuple[object, ...] | list[object], warnings)
        if not all(isinstance(item, str) for item in warning_items):
            raise ValueError("warnings must be strings")
        details = cast(object, self.details)
        if not isinstance(details, dict):
            raise ValueError("details must be an object with string keys")
        if not all(isinstance(key, str) for key in cast(dict[object, object], details)):
            raise ValueError("details must be an object with string keys")
        try:
            json.dumps(self.to_dict(), allow_nan=False)
        except (TypeError, ValueError) as exc:
            raise ValueError("job result must contain JSON values") from exc

    def to_dict(self) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in (
            ("rows_read", self.rows_read), ("rows_written", self.rows_written),
            ("watermark", self.watermark),
        ):
            if value is not None:
                result[key] = value
        if self.warnings:
            result["warnings"] = list(self.warnings)
        if self.details:
            result["details"] = self.details
        return result

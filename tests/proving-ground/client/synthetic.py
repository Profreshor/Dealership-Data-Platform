"""Client wrapper for the proving ground's seeded HTTP service."""

import json
import os
from collections.abc import Mapping
from typing import Any
from urllib.parse import urlencode

from ddp.http import request


class SyntheticClient:
    def __init__(self, settings: Mapping[str, Any]) -> None:
        self.base_url: str = settings["base_url"]

    def customers(self, since: str | None) -> list[dict[str, Any]]:
        query = urlencode({"since": since}) if since else ""
        response = request(
            "GET",
            f"{self.base_url}/customers?{query}",
            auth_header=("X-API-Key", os.environ["SYNTHETIC_API_KEY"]),
            timeout=5,
        )
        rows: list[dict[str, Any]] = json.loads(response.body)
        return rows

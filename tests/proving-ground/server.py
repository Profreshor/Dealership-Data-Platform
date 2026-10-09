"""Seeded local HTTP service; never included in a client or production image."""

import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if urlsplit(self.path).path != "/customers":
            self.send_error(404)
            return
        if self.headers.get("X-API-Key") != os.environ["SYNTHETIC_API_KEY"]:
            self.send_error(401)
            return
        rows = [
            {"id": "one", "name": "Synthetic Customer", "updated_at": "2026-09-04T12:00:00Z"},
            {"id": "two", "name": "Example Customer", "updated_at": "2026-09-04T12:01:00Z"},
        ]
        body = json.dumps(rows).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    ThreadingHTTPServer(
        (os.environ.get("SYNTHETIC_HOST", "127.0.0.1"), 18080), Handler
    ).serve_forever()

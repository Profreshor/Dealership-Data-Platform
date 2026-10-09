"""Small TLS S3 fixture for the real container backup/restore path."""

import os
import ssl
import threading
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlsplit

objects: dict[str, bytes] = {}
lock = threading.Lock()


class Store(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: object) -> None:
        pass

    def reply(self, status: int, body: bytes = b"") -> None:
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        route = urlsplit(self.path)
        if route.path == "/healthz":
            self.reply(200)
            return
        if not self.authorized():
            return
        with lock:
            if route.path.rstrip("/") == "/synthetic-backups" and "list-type" in parse_qs(
                route.query
            ):
                prefix = parse_qs(route.query).get("prefix", [""])[0]
                root = ET.Element(
                    "ListBucketResult", xmlns="http://s3.amazonaws.com/doc/2006-03-01/"
                )
                ET.SubElement(root, "IsTruncated").text = "false"
                for key in sorted(objects):
                    if key.startswith(prefix):
                        ET.SubElement(ET.SubElement(root, "Contents"), "Key").text = key
                self.reply(200, ET.tostring(root))
            else:
                body = objects.get(unquote(route.path.removeprefix("/synthetic-backups/")))
                self.reply(404 if body is None else 200, body or b"")

    def do_PUT(self) -> None:
        if not self.authorized():
            return
        size = int(self.headers["Content-Length"])
        if not 0 <= size <= 8 * 1024 * 1024:
            self.reply(413)
            return
        body = self.rfile.read(size)
        with lock:
            objects[unquote(urlsplit(self.path).path.removeprefix("/synthetic-backups/"))] = body
        self.reply(200)

    def do_DELETE(self) -> None:
        if not self.authorized():
            return
        with lock:
            objects.pop(unquote(urlsplit(self.path).path.removeprefix("/synthetic-backups/")), None)
        self.reply(204)

    def authorized(self) -> bool:
        # ponytail: this private fixture checks credential selection, not AWS's
        # signature algorithm; real storage compatibility belongs to host acceptance.
        if "Credential=synthetic-backup-access/" not in self.headers.get("Authorization", ""):
            self.reply(403)
            return False
        return True


server = ThreadingHTTPServer(("0.0.0.0", 18443), Store)
tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
tls.load_cert_chain(os.environ["TLS_CERTIFICATE"], os.environ["TLS_PRIVATE_KEY"])
server.socket = tls.wrap_socket(server.socket, server_side=True)
server.serve_forever()

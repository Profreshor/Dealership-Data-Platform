"""Small PTY driver shared by native and Compose CLI checks."""

import contextlib
import errno
import fcntl
import os
import pty
import re
import select
import signal
import struct
import termios
import time
from pathlib import Path

ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


class Terminal:
    def __init__(
        self, command: list[str], env: dict[str, str], *, cwd: Path, rows: int = 35
    ) -> None:
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(cwd)
            os.execvpe(command[0], command, {**env, "TERM": "xterm-256color"})
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, 120, 0, 0))
        self.buffer = ""
        self.exited = False

    def wait(self, *texts: str) -> None:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if all(text in ANSI.sub("", self.buffer) for text in texts):
                self.buffer = ""
                return
            ready, _, _ = select.select([self.fd], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(self.fd, 65536)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    break
                if not chunk:
                    break
                self.buffer += chunk.decode(errors="replace")
        raise AssertionError(f"TUI did not show {texts!r}: {self.buffer[-8000:]!r}")

    def send(self, text: str) -> None:
        os.write(self.fd, text.encode())

    def close(self) -> None:
        self.send("q")
        self.finish((0,))

    def finish(self, allowed: tuple[int, ...]) -> None:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.exited = True
                if os.waitstatus_to_exitcode(status) not in allowed:
                    raise AssertionError("TUI did not exit successfully")
                mode = termios.tcgetattr(self.fd)[3]
                if not mode & termios.ECHO or not mode & termios.ICANON:
                    raise AssertionError("TUI did not restore canonical input and echo")
                return
            time.sleep(0.05)
        raise AssertionError("TUI did not quit")

    def cleanup(self) -> None:
        if not self.exited:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(self.pid, signal.SIGTERM)
        os.close(self.fd)
        if not self.exited:
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                if os.waitpid(self.pid, os.WNOHANG)[0]:
                    self.exited = True
                    return
                time.sleep(0.05)
            with contextlib.suppress(ProcessLookupError):
                os.killpg(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)
            self.exited = True

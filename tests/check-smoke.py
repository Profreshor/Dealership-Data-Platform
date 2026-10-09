"""Run the template smoke check; client initialization may replace this runner."""

import subprocess
import sys
from pathlib import Path

runner = Path(__file__).resolve().parent / "proving-ground" / "check-smoke.py"
subprocess.run(
    [sys.executable, str(runner)], cwd=runner.parent, check=True
)

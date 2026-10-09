from collections.abc import Callable

from .context import JobContext
from .result import JobResult


def job[T: Callable[[JobContext], JobResult]](function: T) -> T:
    """Mark a typed function as a DDP job entrypoint."""
    return function

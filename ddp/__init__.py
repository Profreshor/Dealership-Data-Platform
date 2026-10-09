"""The small, typed interface between Go and Python jobs."""

from .context import JobContext, ReadRef, WriteRef
from .job import job
from .result import JobResult

__all__ = ["JobContext", "JobResult", "ReadRef", "WriteRef", "job"]

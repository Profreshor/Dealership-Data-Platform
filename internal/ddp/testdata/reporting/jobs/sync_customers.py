from client.synthetic import SyntheticClient
from ddp import JobContext, JobResult, job, landing


@job
def run(ctx: JobContext) -> JobResult:
    since = ctx.watermark("current")
    rows = SyntheticClient(ctx.integration("synthetic")).customers(since)
    count = landing.upsert(ctx, "synthetic", "customers", rows, key="id")
    watermark = max(row["updated_at"] for row in rows) if rows else since
    ctx.log("customers_loaded", rows_written=count)
    return JobResult(rows_read=len(rows), rows_written=count, watermark=watermark)

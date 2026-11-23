import json
import logging
import os
import threading
import time
from contextlib import closing
from datetime import UTC, date, datetime, timedelta
from pathlib import Path

import boto3
import psycopg
import requests
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError
from flask import Flask
from psycopg.rows import dict_row
from waitress import serve

app = Flask(__name__)
log = logging.getLogger("analytics")
operation_lock = threading.Lock()
sql_dir = Path(__file__).with_name("sql")

clickhouse_url = os.environ["CLICKHOUSE_URL"]
clickhouse_auth = (os.environ["CLICKHOUSE_USER"], os.environ["CLICKHOUSE_PASSWORD"])
postgres_dsn = os.environ["POSTGRES_DSN"]
bucket = os.environ["S3_BUCKET"]
service_errors = (requests.RequestException, psycopg.Error, BotoCoreError, ClientError)


def utc_today():
    return datetime.now(UTC).date()


def connect_postgres():
    return psycopg.connect(
        postgres_dsn, row_factory=dict_row, connect_timeout=3,
        options="-c statement_timeout=10000",
    )


def clickhouse(sql, **parameters):
    response = requests.post(
        clickhouse_url,
        auth=clickhouse_auth,
        params={"wait_end_of_query": 1, **{f"param_{key}": str(value) for key, value in parameters.items()}},
        data=sql.encode("utf-8"),
        timeout=(3, 30),
    )
    if not response.ok:
        log.error("ClickHouse query failed: %s", response.text.strip())
        response.raise_for_status()
    return response.content.decode("utf-8")


def select(sql, **parameters):
    return [
        json.loads(line)
        for line in clickhouse(sql + "\nFORMAT JSONEachRow", **parameters).splitlines()
    ]


def migrate():
    for statement in (sql_dir / "migrate_clickhouse.sql").read_text().strip().rstrip(";").split(";"):
        clickhouse(statement.strip())
    with connect_postgres() as connection:
        connection.execute((sql_dir / "migrate_postgres.sql").read_text())


def calculate_metrics(day, observed_through):
    summary, = select((sql_dir / "summary.sql").read_text(), metric_date=day)
    started, finished = int(summary["started"]), int(summary["finished"])
    metrics = [
        ("dau", "", int(summary["dau"])),
        ("avg_watch_seconds", "", summary["avg_watch_seconds"] if finished else None),
        ("conversion", "", finished / started if started else None),
    ]
    for movie in select((sql_dir / "top_movies.sql").read_text(), metric_date=day):
        metrics.append(("top_movies", movie["movie_id"], int(movie["views"])))

    returns = {
        int(row["day_number"]): int(row["users"])
        for row in select(
            (sql_dir / "retention.sql").read_text(), metric_date=day, observed_through=observed_through,
        )
    }
    cohort_size = returns.get(0, 0)
    for day_number in range(8):
        observed = day + timedelta(days=day_number) <= observed_through
        value = returns.get(day_number, 0) / cohort_size if observed and cohort_size else None
        metrics.append(("retention", str(day_number), value))
    return int(summary["events_processed"]), metrics


def materialize_clickhouse(day, metrics, computed_at):
    rows = "\n".join(
        json.dumps({
            "metric_date": day.isoformat(),
            "metric_name": name,
            "dimension": dimension,
            "value": value,
            "computed_at": computed_at.replace(tzinfo=None).isoformat(sep=" ", timespec="milliseconds"),
        }, allow_nan=False)
        for name, dimension, value in metrics
    )
    clickhouse("TRUNCATE TABLE cinema.daily_metrics_stage")
    clickhouse("INSERT INTO cinema.daily_metrics_stage FORMAT JSONEachRow\n" + rows)
    clickhouse(
        "ALTER TABLE cinema.daily_metrics REPLACE PARTITION {metric_date:Date32}"
        " FROM cinema.daily_metrics_stage",
        metric_date=day,
    )


def enqueue_export(connection, day):
    connection.execute("INSERT INTO pending_exports VALUES (%s) ON CONFLICT DO NOTHING", (day,))


def read_postgres_metrics(connection, day):
    return connection.execute(
        "SELECT metric_name, dimension, value, computed_at FROM daily_metrics"
        " WHERE metric_date = %s ORDER BY metric_name, dimension",
        (day,),
    ).fetchall()


def snapshots_are_current(day, metrics):
    expected = {(name, dimension): value for name, dimension, value in metrics}
    with connect_postgres() as connection:
        postgres_rows = read_postgres_metrics(connection, day)
    if {(row["metric_name"], row["dimension"]): row["value"] for row in postgres_rows} != expected:
        return False

    clickhouse_rows = select(
        "SELECT metric_name, dimension, value FROM cinema.daily_metrics WHERE metric_date = {metric_date:Date32}",
        metric_date=day,
    )
    return {(row["metric_name"], row["dimension"]): row["value"] for row in clickhouse_rows} == expected


def save_postgres(day, metrics, computed_at):
    for attempt in range(3):
        try:
            with connect_postgres() as connection, connection.cursor() as cursor:
                cursor.execute("DELETE FROM daily_metrics WHERE metric_date = %s", (day,))
                cursor.executemany(
                    "INSERT INTO daily_metrics"
                    " (metric_date, metric_name, dimension, value, computed_at)"
                    " VALUES (%s, %s, %s, %s, %s)",
                    [(day, name, dimension, value, computed_at) for name, dimension, value in metrics],
                )
                if day < utc_today():
                    enqueue_export(connection, day)
            return
        except psycopg.OperationalError:
            log.exception("PostgreSQL write failed for %s (attempt %s/3)", day, attempt + 1)
            if attempt == 2:
                raise
            time.sleep(0.5 * 2**attempt)


def aggregate(day, observed_through, skip_unchanged=False):
    with operation_lock:
        started = time.monotonic()
        log.info("Aggregation started: date=%s", day)
        events, metrics = calculate_metrics(day, observed_through)
        metrics_written = 0
        if not skip_unchanged or not snapshots_are_current(day, metrics):
            computed_at = datetime.now(UTC)
            computed_at = computed_at.replace(microsecond=computed_at.microsecond // 1000 * 1000)
            materialize_clickhouse(day, metrics, computed_at)
            save_postgres(day, metrics, computed_at)
            metrics_written = len(metrics)
        log.info(
            "Aggregation completed: date=%s events=%s metrics_written=%s elapsed_seconds=%.3f",
            day, events, metrics_written, time.monotonic() - started,
        )
        return {"date": day.isoformat(), "events_processed": events, "metrics_written": metrics_written}


def s3_client():
    return boto3.client(
        "s3",
        endpoint_url=os.environ["S3_ENDPOINT_URL"],
        region_name="us-east-1",
        config=Config(
            s3={"addressing_style": "path"}, connect_timeout=3, read_timeout=5,
            retries={"total_max_attempts": 1},
        ),
    )


def export(day):
    with operation_lock:
        with connect_postgres() as connection:
            metrics = read_postgres_metrics(connection, day)
            if not metrics:
                return None
            enqueue_export(connection, day)

        payload = {
            "date": day.isoformat(),
            "metrics": [
                {**row, "computed_at": row["computed_at"].astimezone(UTC).isoformat()}
                for row in metrics
            ],
        }
        object_key = f"daily/{day.isoformat()}/aggregates.json"
        with closing(s3_client()) as s3:
            try:
                s3.head_bucket(Bucket=bucket)
            except ClientError as error:
                if error.response["ResponseMetadata"]["HTTPStatusCode"] != 404:
                    raise
                s3.create_bucket(Bucket=bucket)
            s3.put_object(
                Bucket=bucket,
                Key=object_key,
                Body=json.dumps(payload, ensure_ascii=False, allow_nan=False).encode("utf-8"),
                ContentType="application/json",
            )

        with connect_postgres() as connection:
            connection.execute("DELETE FROM pending_exports WHERE metric_date = %s", (day,))
        object_uri = f"s3://{bucket}/{object_key}"
        log.info("Export completed: object=%s metrics=%s", object_uri, len(metrics))
        return {"date": day.isoformat(), "object": object_uri, "metrics_exported": len(metrics)}


def schedule(interval):
    while True:
        yesterday = utc_today() - timedelta(days=1)
        # Earlier cohorts can gain another observed day as time passes.
        for offset in range(8):
            day = yesterday - timedelta(days=offset)
            try:
                aggregate(day, observed_through=yesterday, skip_unchanged=True)
            except service_errors:
                log.exception("Scheduled aggregation failed: date=%s", day)

        try:
            with connect_postgres() as connection:
                pending = connection.execute(
                    "SELECT metric_date FROM pending_exports ORDER BY metric_date"
                ).fetchall()
            for row in pending:
                try:
                    export(row["metric_date"])
                except service_errors:
                    log.exception("Scheduled export failed: date=%s", row["metric_date"])
        except psycopg.Error:
            log.exception("Could not read pending exports")
        time.sleep(interval)


def parse_date(value):
    try:
        day = date.fromisoformat(value)
    except ValueError:
        return None
    if value == day.isoformat() and date(1900, 1, 1) <= day <= utc_today():
        return day
    return None


def invalid_date():
    return {"error": "expected a date from 1900-01-01 through today (UTC) in YYYY-MM-DD format"}, 400


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/aggregate/<value>")
def aggregate_endpoint(value):
    day = parse_date(value)
    if day is None:
        return invalid_date()
    try:
        return aggregate(day, observed_through=max(day, utc_today() - timedelta(days=1)))
    except service_errors:
        log.exception("Manual aggregation failed: date=%s", value)
        return {"error": "aggregation failed"}, 503


@app.post("/export/<value>")
def export_endpoint(value):
    day = parse_date(value)
    if day is None:
        return invalid_date()
    try:
        result = export(day)
        if result is None:
            return {"error": "no aggregate exists for this date"}, 404
        return result
    except service_errors:
        log.exception("Manual export failed: date=%s", value)
        return {"error": "export failed"}, 503


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    interval = int(os.environ.get("AGGREGATION_INTERVAL_SECONDS", "60"))
    if interval <= 0:
        raise ValueError("AGGREGATION_INTERVAL_SECONDS must be positive")
    migrate()
    threading.Thread(target=schedule, args=(interval,), daemon=True).start()
    serve(app, host="0.0.0.0", port=8001)

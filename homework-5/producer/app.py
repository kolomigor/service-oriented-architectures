import json
import logging
import os
import struct
from datetime import UTC, datetime
from io import BytesIO
from pathlib import Path
from threading import Thread
from uuid import UUID

import requests
from confluent_kafka import KafkaException, Producer
from fastavro import parse_schema, schemaless_writer
from fastavro.validation import validate
from flask import Flask, request
from waitress import serve

from .demo import demo_events

TOPIC = "movie-events"
SUBJECT = f"{TOPIC}-value"
SCHEMA_TEXT = (Path(__file__).resolve().parents[1] / "schemas/movie-event.avsc").read_text()
SCHEMA = parse_schema(json.loads(SCHEMA_TEXT))
FIELDS = {field["name"] for field in SCHEMA["fields"]}
log = logging.getLogger(__name__)


class PublicationError(Exception):
    pass


def register_schema(registry_url):
    subject_url = f"{registry_url.rstrip('/')}/subjects/{SUBJECT}"
    payload = {"schema": SCHEMA_TEXT, "schemaType": "AVRO"}
    response = requests.post(f"{subject_url}/versions", json=payload, timeout=5)
    response.raise_for_status()
    response = requests.post(subject_url, json=payload, timeout=5)
    response.raise_for_status()
    registered = response.json()
    log.info("registered subject=%s version=%s schema_id=%s", SUBJECT, registered["version"], registered["id"])
    return registered["id"]


def normalize_event(body):
    if not isinstance(body, dict) or body.keys() != FIELDS:
        raise ValueError(f"JSON must contain these fields: {', '.join(sorted(FIELDS))}")

    for name in ("user_id", "movie_id", "session_id"):
        if not isinstance(body[name], str) or not body[name].strip():
            raise ValueError(f"{name} must be a non-empty string")

    try:
        event_id = str(UUID(body["event_id"]))
    except (AttributeError, TypeError, ValueError) as error:
        raise ValueError("event_id must be a UUID string") from error

    try:
        timestamp = datetime.fromisoformat(body["timestamp"])
        if timestamp.tzinfo is None:
            raise ValueError("timestamp has no time zone")
        timestamp = timestamp.astimezone(UTC)
    except (TypeError, ValueError, OverflowError) as error:
        raise ValueError("timestamp must be an ISO 8601 datetime with a time zone") from error

    if not 1900 <= timestamp.year <= 2299:
        raise ValueError("timestamp year in UTC must be between 1900 and 2299")
    timestamp = timestamp.replace(microsecond=timestamp.microsecond // 1000 * 1000)

    progress = body["progress_seconds"]
    if type(progress) is not int or not 0 <= progress <= 2**31 - 1:
        raise ValueError("progress_seconds must be a nonnegative 32-bit integer")

    return {**body, "event_id": event_id, "timestamp": timestamp}


def encode_event(event, schema_id):
    if not validate(event, SCHEMA, raise_errors=False, strict=True):
        raise ValueError("event does not match the Avro schema")

    # AvroConfluent framing adds the schema ID before the Avro payload.
    buffer = BytesIO()
    buffer.write(struct.pack(">BI", 0, schema_id))
    schemaless_writer(buffer, SCHEMA, event, strict=True)
    return buffer.getvalue()


class Publisher:
    def __init__(self, brokers, schema_id):
        self.schema_id = schema_id
        self.producer = Producer({
            "bootstrap.servers": brokers,
            "acks": "all",
            "enable.idempotence": True,
            "retry.backoff.ms": 100,
            "retry.backoff.max.ms": 1000,
            "message.timeout.ms": 15000,
            "linger.ms": 0,
        })

    def publish(self, body):
        event = normalize_event(body)
        value = encode_event(event, self.schema_id)
        delivery = []

        def on_delivery(error, message):
            delivery.append((error, message))

        try:
            self.producer.produce(
                TOPIC,
                key=event["user_id"].encode("utf-8"),
                value=value,
                on_delivery=on_delivery,
            )
            while not delivery:
                self.producer.poll(0.1)
        except (BufferError, KafkaException) as error:
            raise PublicationError(str(error)) from error

        error, message = delivery[0]
        if error is not None:
            raise PublicationError(str(error))

        log.info(
            "published event_id=%s event_type=%s timestamp=%s partition=%s offset=%s",
            event["event_id"], event["event_type"], event["timestamp"].isoformat(),
            message.partition(), message.offset(),
        )
        return event["event_id"]


def create_app(publisher, demo_enabled=False):
    app = Flask(__name__)
    demo_status = "disabled"

    def publish_demo():
        nonlocal demo_status
        today = datetime.now(UTC).date()
        count = 0
        try:
            for event in demo_events(today):
                publisher.publish(event)
                count += 1
        except PublicationError:
            log.exception("demo data publication failed after %s events", count)
            demo_status = "failed"
        else:
            log.info("published %s demo events", count)
            demo_status = "complete"

    if demo_enabled:
        demo_status = "publishing"
        Thread(target=publish_demo, daemon=True, name="demo-generator").start()

    @app.get("/health")
    def health():
        return {"status": "ok", "demo": demo_status}

    @app.post("/events")
    def post_event():
        try:
            event_id = publisher.publish(request.get_json(silent=True))
        except ValueError as error:
            return {"error": str(error)}, 400
        except PublicationError as error:
            log.error("event publication failed: %s", error)
            return {"error": "Kafka did not acknowledge the event"}, 503
        return {"event_id": event_id}, 201

    return app


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    schema_id = register_schema(os.environ["SCHEMA_REGISTRY_URL"])
    publisher = Publisher(os.environ["KAFKA_BOOTSTRAP_SERVERS"], schema_id)
    demo_enabled = os.getenv("DEMO_GENERATOR", "false").lower() == "true"
    serve(create_app(publisher, demo_enabled), host="0.0.0.0", port=8000)

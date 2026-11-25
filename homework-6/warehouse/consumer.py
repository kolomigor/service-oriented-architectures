import base64
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import logging
import os
import signal
from threading import Event, Thread
import time
from uuid import UUID

from cassandra import InvalidRequest, OperationTimedOut, ReadFailure, ReadTimeout, Unavailable, WriteFailure, WriteTimeout
from cassandra.cluster import NoHostAvailable
from confluent_kafka import Consumer, KafkaException, Producer, TopicPartition
from confluent_kafka.admin import AdminClient
from prometheus_client import CollectorRegistry, Counter, Histogram, generate_latest
from prometheus_client.exposition import CONTENT_TYPE_LATEST

from warehouse.config import DLQ_TOPIC, GROUP, KAFKA_BOOTSTRAP, SCHEMA_REGISTRY_URL
from warehouse.domain import calculate_mutation, event_id
from warehouse.events import EVENT_TOPIC, InvalidEvent, Registry, RegistryUnavailable
from warehouse.lag import KafkaLag
from warehouse.store import Store, cassandra_cluster

METRICS = CollectorRegistry()
EVENTS = Counter("events_processed_total", "Events applied in Cassandra", ["event_type"], registry=METRICS)
DURATION = Histogram("event_processing_duration_seconds", "Time handling a Kafka record", registry=METRICS)
WRITE_ERRORS = Counter("cassandra_write_errors_total", "Failed Cassandra write attempts", registry=METRICS)
CASSANDRA_ERRORS = (NoHostAvailable, OperationTimedOut, ReadFailure, ReadTimeout,
                    Unavailable, WriteFailure, WriteTimeout)
KAFKA = {"bootstrap.servers": KAFKA_BOOTSTRAP}
# The event topic has one partition. Its offset orders application writes.
WRITE_EPOCH_US = 1_700_000_000_000_000


def write(operation, *args):
    try:
        operation(*args)
    except (*CASSANDRA_ERRORS, InvalidRequest):
        WRITE_ERRORS.inc()
        raise


def apply_event(store, event, version, offset):
    identifier = event_id(event)
    record = store.event(identifier)
    if record and record.applied:
        return "duplicate"
    if record:
        mutation = json.loads(record.mutation_json)
    else:
        mutation = calculate_mutation(store, event, version, WRITE_EPOCH_US + offset)
        if mutation is None:
            return "stale"
        write(store.save_mutation, identifier, mutation)

    try:
        write(store.apply_mutation, mutation)
    except InvalidRequest as error:
        if "batch too large" in str(error).lower():
            raise InvalidEvent("Order exceeds the Cassandra batch size limit") from error
        raise
    write(store.mark_applied, identifier)
    EVENTS.labels(event["event_type"]).inc()
    return "applied"


def send_to_dlq(producer, message, event, error):
    location = {"topic": message.topic(), "partition": message.partition(), "offset": message.offset()}
    envelope = {
        "original_event": event,
        "error_reason": str(error),
        "error_code": "VALIDATION_ERROR" if event is not None else "DESERIALIZATION_ERROR",
        "failed_at": datetime.now(timezone.utc).isoformat(),
        "kafka_metadata": location,
    }
    if event is None:
        raw = message.value()
        envelope["original_payload_base64"] = None if raw is None else base64.b64encode(raw).decode()

    delivery = []
    producer.produce(
        DLQ_TOPIC,
        key=f"{message.topic()}:{message.partition()}:{message.offset()}",
        value=json.dumps(envelope).encode(),
        on_delivery=lambda error, _: delivery.append(error),
    )
    if producer.flush(6) or not delivery:
        raise TimeoutError("DLQ publication was not acknowledged")
    if delivery[0] is not None:
        raise KafkaException(delivery[0])


def handle(store, registry, producer, message):
    event = None
    try:
        event, version = registry.decode(message.value())
        result = apply_event(store, event, version, message.offset())
    except InvalidEvent as error:
        send_to_dlq(producer, message, event, error)
        result = "dlq"
    fields = event or {}
    logging.info(
        "event_id=%s event_type=%s partition=%s offset=%s result=%s",
        fields.get("event_id"), fields.get("event_type"), message.partition(), message.offset(), result,
    )


def serve_http(store):
    admin = AdminClient(KAFKA)
    METRICS.register(KafkaLag())

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/metrics":
                status, body, content_type = 200, generate_latest(METRICS), CONTENT_TYPE_LATEST
            elif self.path == "/health":
                status, body, content_type = 200, b"ok\n", "text/plain"
                try:
                    topic = admin.list_topics(EVENT_TOPIC, timeout=2).topics[EVENT_TOPIC]
                    if topic.error:
                        raise KafkaException(topic.error)
                    store.event(UUID(int=0), timeout=2)
                except (KafkaException, KeyError, *CASSANDRA_ERRORS):
                    status, body = 503, b"unavailable\n"
            else:
                status, body, content_type = 404, b"not found\n", "text/plain"
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    server = ThreadingHTTPServer(("0.0.0.0", int(os.getenv("HTTP_PORT", "8080"))), Handler)
    Thread(target=server.serve_forever, daemon=True).start()
    return server


def consume(store, stop):
    consumer = Consumer({
        **KAFKA,
        "group.id": GROUP,
        "enable.auto.commit": False,
        "enable.auto.offset.store": False,
        "auto.offset.reset": "earliest",
    })
    producer = Producer({**KAFKA, "acks": "all", "enable.idempotence": True,
                         "message.timeout.ms": 5000})
    registry = Registry(SCHEMA_REGISTRY_URL)
    consumer.subscribe([EVENT_TOPIC])
    try:
        while not stop.is_set():
            message = consumer.poll(0.5)
            if message is None:
                continue
            if message.error():
                logging.warning("Kafka poll: %s", message.error())
                continue

            started = time.monotonic()
            while not stop.is_set():
                try:
                    handle(store, registry, producer, message)
                    break
                except (RegistryUnavailable, KafkaException, BufferError, TimeoutError, *CASSANDRA_ERRORS) as error:
                    logging.warning("Retrying offset %s: %s", message.offset(), error)
                    if time.monotonic() - started > 90:
                        raise
                    stop.wait(1)
            if stop.is_set():
                break
            DURATION.observe(time.monotonic() - started)
            committed = consumer.commit(
                offsets=[TopicPartition(message.topic(), message.partition(), message.offset() + 1)],
                asynchronous=False,
            )
            if committed[0].error:
                raise KafkaException(committed[0].error)
    finally:
        consumer.close()


def main():
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    stop = Event()
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, lambda *_: stop.set())
    with cassandra_cluster() as cluster:
        store = Store.connect(cluster)
        server = serve_http(store)
        try:
            consume(store, stop)
        finally:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    main()

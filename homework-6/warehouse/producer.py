import argparse
import json
import sys
from datetime import UTC, datetime
from uuid import uuid4

from confluent_kafka import KafkaException, Producer

from .config import KAFKA_BOOTSTRAP, SCHEMA_REGISTRY_URL
from .events import EVENT_TOPIC, InvalidEvent, Registry, RegistryUnavailable


def publish(kafka, registry, event, version=1):
    if not isinstance(event, dict):
        raise InvalidEvent("Expected a JSON object")
    event = {
        "event_id": str(uuid4()),
        "timestamp": datetime.now(UTC).isoformat(),
        **event,
    }
    payload = registry.encode(event, version)
    delivery_errors = []

    def on_delivery(error, _message):
        if error is not None:
            delivery_errors.append(error)

    kafka.produce(EVENT_TOPIC, value=payload, key=event["event_id"], on_delivery=on_delivery)
    if kafka.flush(15):
        raise KafkaException("Kafka did not acknowledge the event")
    if delivery_errors:
        raise KafkaException(delivery_errors[0])
    return event


def main():
    parser = argparse.ArgumentParser(description="Send one warehouse event to Kafka")
    parser.add_argument("file", type=argparse.FileType("r", encoding="utf-8"), help="JSON event, or - for stdin")
    parser.add_argument("--version", type=int, choices=(1, 2), default=1, help="Avro writer schema version")
    args = parser.parse_args()

    try:
        event = json.load(args.file)
        kafka = Producer({
            "bootstrap.servers": KAFKA_BOOTSTRAP,
            "enable.idempotence": True,
            "message.timeout.ms": 10000,
        })
        registry = Registry(SCHEMA_REGISTRY_URL)
        print(json.dumps(publish(kafka, registry, event, args.version), ensure_ascii=False))
    except (ValueError, RegistryUnavailable, KafkaException) as error:
        print(f"Could not publish event: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

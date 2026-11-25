import json
from io import BytesIO
from pathlib import Path
from struct import Struct

import requests
from fastavro import parse_schema, schemaless_reader, schemaless_writer
from fastavro.validation import validate

EVENT_TOPIC = "warehouse-events"
SCHEMA_DIR = Path(__file__).resolve().parents[1] / "schemas"
SCHEMA_FILES = {
    ("WarehouseEvent", 1): "warehouse-event-v1.avsc",
    ("ProductReceived", 1): "product-received-v1.avsc",
    ("ProductReceived", 2): "product-received-v2.avsc",
}
HEADER = Struct(">BI")  # Confluent magic byte followed by a big-endian schema ID.


class InvalidEvent(ValueError):
    pass


class RegistryUnavailable(Exception):
    pass


class Registry:
    def __init__(self, url):
        self.url = url.rstrip("/")
        self.writers = {}
        self.readers = {}

    def _request(self, method, path, payload=None, *, unknown_schema_is_invalid=False):
        try:
            response = requests.request(method, self.url + path, json=payload, timeout=5)
        except requests.RequestException as error:
            raise RegistryUnavailable(f"Schema Registry request failed: {error}") from error

        if unknown_schema_is_invalid and response.status_code == 404:
            raise InvalidEvent(f"Unknown Avro schema ID: {path.rsplit('/', 1)[-1]}")
        if not response.ok:
            raise RegistryUnavailable(
                f"Schema Registry {method} {path}: HTTP {response.status_code} {response.text}"
            )
        try:
            return response.json()
        except ValueError as error:
            raise RegistryUnavailable("Schema Registry returned invalid JSON") from error

    def register_schemas(self):
        self._request(
            "PUT",
            f"/config/{EVENT_TOPIC}-ProductReceived",
            {"compatibility": "BACKWARD"},
        )
        for (name, _), filename in SCHEMA_FILES.items():
            self._request(
                "POST",
                f"/subjects/{EVENT_TOPIC}-{name}/versions",
                {"schema": (SCHEMA_DIR / filename).read_text(encoding="utf-8")},
            )

    def encode(self, event, version=1):
        name = "ProductReceived" if event.get("event_type") == "PRODUCT_RECEIVED" else "WarehouseEvent"
        key = (name, version)
        if key not in SCHEMA_FILES:
            raise InvalidEvent("Version 2 is available only for PRODUCT_RECEIVED")
        if key not in self.writers:
            schema_text = (SCHEMA_DIR / SCHEMA_FILES[key]).read_text(encoding="utf-8")
            result = self._request("POST", f"/subjects/{EVENT_TOPIC}-{name}", {"schema": schema_text})
            self.writers[key] = (result["id"], parse_schema(json.loads(schema_text)))
        schema_id, schema = self.writers[key]

        if not validate(event, schema, raise_errors=False):
            raise InvalidEvent(f"Event does not match the Avro schema {name} V{version}")
        buffer = BytesIO()
        buffer.write(HEADER.pack(0, schema_id))
        try:
            schemaless_writer(buffer, schema, event, strict_allow_default=True)
        except (TypeError, ValueError, OverflowError) as error:
            raise InvalidEvent(str(error)) from error
        return buffer.getvalue()

    def decode(self, payload):
        if payload is None or len(payload) <= HEADER.size or payload[0] != 0:
            raise InvalidEvent("Expected Confluent Avro framing")
        _, schema_id = HEADER.unpack_from(payload)
        if schema_id not in self.readers:
            result = self._request(
                "GET", f"/schemas/ids/{schema_id}", unknown_schema_is_invalid=True
            )
            if result.get("schemaType", "AVRO") != "AVRO":
                raise InvalidEvent(f"Schema {schema_id} is not Avro")
            writer = json.loads(result["schema"])
            name = writer.get("name") if isinstance(writer, dict) else None
            if name not in {"WarehouseEvent", "ProductReceived"}:
                raise InvalidEvent(f"Unsupported Avro record: {name}")
            fields = {field["name"] for field in writer["fields"]}
            version = 2 if name == "ProductReceived" and "supplier_id" in fields else 1
            self.readers[schema_id] = (parse_schema(writer), version)
        schema, version = self.readers[schema_id]

        buffer = BytesIO(payload[HEADER.size:])
        try:
            event = schemaless_reader(buffer, schema)
        except (EOFError, ValueError, TypeError, IndexError, OverflowError) as error:
            raise InvalidEvent(f"Invalid Avro payload: {error}") from error
        if buffer.read(1):
            raise InvalidEvent("Unexpected data after Avro event")
        if (schema["name"] == "ProductReceived") != (event.get("event_type") == "PRODUCT_RECEIVED"):
            raise InvalidEvent("Event type does not match its Avro record")
        return event, version

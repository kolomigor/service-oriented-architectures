import json
import os
import time
import unittest
from datetime import UTC, datetime
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from uuid import uuid4

PRODUCER_URL = os.getenv("PRODUCER_URL", "http://producer:8000")
CLICKHOUSE_URL = os.getenv("CLICKHOUSE_URL", "http://clickhouse:8123")
CLICKHOUSE_USER = os.getenv("CLICKHOUSE_USER", "cinema")
CLICKHOUSE_PASSWORD = os.getenv("CLICKHOUSE_PASSWORD", "cinema-local")


def post_event(event):
    request = Request(
        f"{PRODUCER_URL}/events",
        data=json.dumps(event).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urlopen(request, timeout=30) as response:
        return response.status, json.load(response)


def read_event(event_id):
    query = """
        SELECT event_id, user_id, movie_id, event_type, timestamp,
               device_type, session_id, progress_seconds
        FROM cinema.raw_events FINAL
        WHERE event_id = {event_id:UUID}
        FORMAT JSONEachRow
    """
    params = urlencode({"param_event_id": event_id})
    request = Request(
        f"{CLICKHOUSE_URL}/?{params}",
        data=query.encode(),
        headers={
            "X-ClickHouse-User": CLICKHOUSE_USER,
            "X-ClickHouse-Key": CLICKHOUSE_PASSWORD,
        },
    )
    with urlopen(request, timeout=5) as response:
        return [json.loads(line) for line in response]


def fixture():
    event_id = str(uuid4())
    return {
        "event_id": event_id,
        "user_id": f"test-user-{event_id}",
        "movie_id": "test-movie",
        "event_type": "VIEW_FINISHED",
        "timestamp": "2021-06-01T15:20:30.456+03:00",
        "device_type": "TV",
        "session_id": f"test-session-{event_id}",
        "progress_seconds": 720,
    }


class PipelineTest(unittest.TestCase):
    def test_event_reaches_clickhouse(self):
        event = fixture()
        status, result = post_event(event)
        self.assertEqual(status, 201)
        self.assertEqual(result["event_id"], event["event_id"])

        # Kafka Engine writes in batches, so the row may not be visible immediately.
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            rows = read_event(event["event_id"])
            if rows:
                break
            time.sleep(0.5)
        else:
            self.fail(f"ClickHouse did not receive event {event['event_id']}")

        self.assertEqual(len(rows), 1)
        stored = rows[0]
        stored["timestamp"] = datetime.fromisoformat(stored["timestamp"]).replace(tzinfo=UTC)
        event["timestamp"] = datetime.fromisoformat(event["timestamp"]).astimezone(UTC)
        self.assertEqual(stored, event)

    def test_invalid_event_is_rejected(self):
        event = fixture()
        event["event_type"] = "INVALID"
        with self.assertRaises(HTTPError) as error:
            post_event(event)
        self.assertEqual(error.exception.code, 400)
        self.assertEqual(read_event(event["event_id"]), [])


if __name__ == "__main__":
    unittest.main(verbosity=2)

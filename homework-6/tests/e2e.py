"""Run the eight scenarios from TASK.md against a running Compose stack."""

import argparse
import json
import os
import re
import subprocess
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import urlopen
from uuid import uuid4

ROOT = Path(__file__).resolve().parents[1]
RUN = uuid4().hex[:8]
KAFKA_BIN = "/opt/kafka/bin"
CONSUMER = f"http://localhost:{os.getenv('CONSUMER_PORT', '8080')}"
REGISTRY = f"http://localhost:{os.getenv('SCHEMA_REGISTRY_PORT', '8081')}"
PROMETHEUS = f"http://localhost:{os.getenv('PROMETHEUS_PORT', '9090')}"
GRAFANA = f"http://localhost:{os.getenv('GRAFANA_PORT', '3000')}"


def compose(*args, data=None, check=True):
    result = subprocess.run(
        ["docker", "compose", *args],
        cwd=ROOT,
        input=data,
        text=True,
        capture_output=True,
        timeout=120,
        check=False,
    )
    if check and result.returncode:
        raise RuntimeError(f"docker compose {' '.join(args)}\n{result.stdout}{result.stderr}")
    return result


def request(url):
    with urlopen(url, timeout=3) as response:
        return response.read().decode()


def status(url):
    try:
        with urlopen(url, timeout=3) as response:
            return response.status
    except HTTPError as error:
        return error.code


def wait_for(description, check, timeout=60):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = check()
            if last:
                return last
        except (URLError, ConnectionError, TimeoutError) as error:
            last = error
        time.sleep(1)
    raise AssertionError(f"{description}. Последний результат: {last!r}")


def publish(event_type, version=1, **data):
    event = {
        "event_type": event_type,
        "event_id": str(uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        **data,
    }
    compose(
        "run", "--rm", "-T", "--no-deps", "producer", "-", "--version", str(version),
        data=json.dumps(event),
    )
    return event


def cql(statement, consistency="QUORUM", check=True):
    return compose(
        "exec", "-T", "cassandra-1", "cqlsh", "-e",
        f"CONSISTENCY {consistency}; {statement};",
        check=check,
    )


def row(table, columns="*", **key):
    # Test keys are generated locally and consist of ASCII letters, digits and dashes.
    predicates = " AND ".join(f"{column} = '{value}'" for column, value in key.items())
    output = cql(f"SELECT JSON {columns} FROM warehouse.{table} WHERE {predicates}").stdout
    rows = [json.loads(line.strip()) for line in output.splitlines() if line.strip().startswith("{")]
    assert len(rows) <= 1, rows
    return rows[0] if rows else {}


def zone_stock(product, zone="A"):
    stock = row("inventory_by_product_zone", product_id=product, zone_id=zone)
    return stock.get("available_quantity"), stock.get("reserved_quantity")


def expect_stock(product, available, reserved=0, zone="A"):
    wait_for(
        f"{product} / {zone}: ожидался остаток {available}, резерв {reserved}",
        lambda: zone_stock(product, zone) == (available, reserved),
    )


def sku(suffix):
    return f"E2E-{RUN}-{suffix}"


def operation(event_type, product, quantity, zone="A", **extra):
    return publish(event_type, product_id=product, zone_id=zone, quantity=quantity, **extra)


def warehouse_cycle():
    product = sku("001")
    second_product = sku("001-extra")
    order = sku("ORDER")
    operation("PRODUCT_RECEIVED", product, 100)
    expect_stock(product, 100)
    wait_for(
        "после приёмки общий остаток равен 100",
        lambda: row("inventory_by_product", product_id=product).get("total_available") == 100,
    )

    operation("PRODUCT_RESERVED", product, 30)
    expect_stock(product, 70, 30)
    publish("PRODUCT_MOVED", product_id=product, from_zone_id="A", to_zone_id="B", quantity=20)
    expect_stock(product, 50, 30)
    expect_stock(product, 20, zone="B")

    operation("PRODUCT_SHIPPED", product, 10)
    expect_stock(product, 40, 30)
    operation("PRODUCT_RECEIVED", second_product, 12, zone="B")
    publish("ORDER_CREATED", order_id=order, items=[
        {"product_id": product, "zone_id": "A", "quantity": 15},
        {"product_id": second_product, "zone_id": "B", "quantity": 4},
    ])
    expect_stock(product, 25, 45)
    expect_stock(second_product, 8, 4, zone="B")
    wait_for("заказ создан", lambda: row("orders_by_id", "status", order_id=order).get("status") == "CREATED")

    publish("ORDER_COMPLETED", order_id=order)
    expect_stock(product, 25, 30)
    expect_stock(second_product, 8, zone="B")
    wait_for("заказ завершён", lambda: row("orders_by_id", "status", order_id=order).get("status") == "COMPLETED")

    operation("PRODUCT_RELEASED", product, 5)
    expect_stock(product, 30, 25)
    publish("INVENTORY_COUNTED", product_id=product, zone_id="A", counted_quantity=0)
    expect_stock(product, 0, 25)


def idempotency():
    product = sku("002")
    event = operation("PRODUCT_RECEIVED", product, 50)
    expect_stock(product, 50)
    compose("restart", "consumer")
    wait_for("consumer готов после перезапуска", lambda: status(CONSUMER + "/health") == 200)
    publish(**event)

    # The barrier confirms that the consumer passed the duplicate in the single partition.
    barrier = sku("002-next")
    operation("PRODUCT_RECEIVED", barrier, 1)
    expect_stock(barrier, 1)
    assert zone_stock(product) == (50, 0)


def consistent_tables():
    product = sku("003")
    operation("PRODUCT_RECEIVED", product, 100)
    expect_stock(product, 100)
    total = wait_for("общая строка товара", lambda: row("inventory_by_product", product_id=product))
    by_zone = wait_for("строка товара в зоне", lambda: row("inventory_by_zone", zone_id="A", product_id=product))
    assert (total["total_available"], total["total_reserved"]) == (100, 0), total
    assert (by_zone["available_quantity"], by_zone["reserved_quantity"]) == (100, 0), by_zone


def out_of_order():
    product = sku("004")
    start = datetime.now(timezone.utc).replace(hour=12, minute=0, second=0, microsecond=0)
    operation("PRODUCT_RECEIVED", product, 100, timestamp=start.isoformat())
    operation("PRODUCT_SHIPPED", product, 20, timestamp=(start + timedelta(minutes=5)).isoformat())
    expect_stock(product, 80)
    operation("PRODUCT_RECEIVED", product, 50, timestamp=(start + timedelta(minutes=2)).isoformat())
    barrier = sku("004-next")
    operation("PRODUCT_RECEIVED", barrier, 1)
    expect_stock(barrier, 1)
    assert zone_stock(product) == (80, 0)


def dead_letter_queue():
    product = sku("005")
    bad = operation("PRODUCT_SHIPPED", product, -5)
    operation("PRODUCT_RECEIVED", product, 5)
    expect_stock(product, 5)
    result = compose(
        "exec", "-T", "kafka", KAFKA_BIN + "/kafka-console-consumer.sh",
        "--bootstrap-server", "kafka:9092", "--topic", "warehouse-events-dlq",
        "--partition", "0", "--offset", "earliest", "--timeout-ms", "4000",
        check=False,
    )
    records = [json.loads(line) for line in result.stdout.splitlines() if line.startswith("{")]
    record = next((
        item for item in records
        if isinstance(item.get("original_event"), dict)
        and item["original_event"].get("event_id") == bad["event_id"]
    ), None)
    assert record, f"Событие {bad['event_id']} отсутствует в DLQ. {result.stderr}"
    assert record["original_event"]["quantity"] == -5
    assert record["error_reason"] and record["error_code"] and record["failed_at"]
    assert record["kafka_metadata"]["partition"] == 0
    assert isinstance(record["kafka_metadata"]["offset"], int)


def live_nodes():
    result = compose("exec", "-T", "cassandra-1", "nodetool", "status")
    return len(re.findall(r"^UN\s", result.stdout, re.MULTILINE))


def failover():
    product = sku("006")
    wait_for("все три ноды Cassandra в статусе UN", lambda: live_nodes() == 3)
    operation("PRODUCT_RECEIVED", product, 200)
    expect_stock(product, 200)
    try:
        compose("stop", "cassandra-2")
        wait_for("Cassandra обнаружила отключение ноды", lambda: live_nodes() == 2)
        operation("PRODUCT_SHIPPED", product, 50)
        expect_stock(product, 150)
        statement = (
            "SELECT available_quantity FROM warehouse.inventory_by_product_zone "
            f"WHERE product_id = '{product}' AND zone_id = 'A'"
        )
        one = cql(statement, "ONE")
        quorum = cql(statement, "QUORUM")
        all_replicas = cql(statement, "ALL", check=False)
        assert re.search(r"^\s*150\s*$", one.stdout, re.MULTILINE), one.stdout
        assert re.search(r"^\s*150\s*$", quorum.stdout, re.MULTILINE), quorum.stdout
        assert "unavailable" in (all_replicas.stdout + all_replicas.stderr).lower()
    finally:
        compose("start", "cassandra-2")
        wait_for("кластер восстановился до трёх нод UN", lambda: live_nodes() == 3, timeout=120)


def metric_total(metrics, name):
    pattern = rf"^{name}(?:\{{[^\n]*\}})?\s+(\S+)"
    return sum(float(number) for number in re.findall(pattern, metrics, re.MULTILINE))


def lag():
    query = urlencode({"query": 'consumer_lag{job="lag"}'})
    payload = json.loads(request(f"{PROMETHEUS}/api/v1/query?{query}"))
    samples = payload["data"]["result"]
    return sum(float(sample["value"][1]) for sample in samples) if samples else -1


def lag_alert_firing():
    payload = json.loads(request(PROMETHEUS + "/api/v1/alerts"))
    return any(
        alert["labels"].get("alertname") == "WarehouseConsumerLag" and alert["state"] == "firing"
        for alert in payload["data"]["alerts"]
    )


def monitoring():
    assert status(CONSUMER + "/health") == 200
    assert status(GRAFANA + "/api/health") == 200
    dashboard = json.loads(request(GRAFANA + "/api/dashboards/uid/warehouse"))["dashboard"]
    assert {panel["id"] for panel in dashboard["panels"]} == {1, 2, 3}
    before = request(CONSUMER + "/metrics")
    for name in (
        "consumer_lag",
        "events_processed_total",
        "event_processing_duration_seconds",
        "cassandra_write_errors_total",
    ):
        assert re.search(rf"^# TYPE {name} ", before, re.MULTILINE), name
    count_before = metric_total(before, "events_processed_total")

    product = sku("007")
    operation("PRODUCT_RECEIVED", product, 20)
    for _ in range(9):
        operation("PRODUCT_SHIPPED", product, 1)
    expect_stock(product, 11)
    wait_for(
        "счётчик обработанных событий вырос на 10",
        lambda: metric_total(request(CONSUMER + "/metrics"), "events_processed_total") >= count_before + 10,
    )
    wait_for("lag до остановки равен нулю", lambda: lag() == 0)

    try:
        compose("stop", "consumer")
        for _ in range(10):
            operation("PRODUCT_RECEIVED", product, 1)
        wait_for("lag остановленного consumer вырос на 10", lambda: lag() >= 10)
        wait_for("алерт WarehouseConsumerLag перешёл в firing", lag_alert_firing)
    finally:
        compose("start", "consumer")
    wait_for("consumer готов после запуска", lambda: status(CONSUMER + "/health") == 200)
    expect_stock(product, 21)
    wait_for("consumer обработал накопившиеся события", lambda: lag() == 0)
    wait_for("алерт перестал действовать", lambda: not lag_alert_firing())


def schema_evolution():
    v1, v2, old_again = (sku(suffix) for suffix in ("008-v1", "008-v2", "008-v1-again"))
    operation("PRODUCT_RECEIVED", v1, 5, version=1)
    operation("PRODUCT_RECEIVED", v2, 7, version=2, supplier_id="SUP-001")
    operation("PRODUCT_RECEIVED", old_again, 3, version=1)
    expect_stock(old_again, 3)
    for product, available, supplier in [(v1, 5, None), (v2, 7, "SUP-001"), (old_again, 3, None)]:
        stock = row("inventory_by_product_zone", product_id=product, zone_id="A")
        assert (stock["available_quantity"], stock["supplier_id"]) == (available, supplier)
    versions = json.loads(request(REGISTRY + "/subjects/warehouse-events-ProductReceived/versions"))
    assert versions == [1, 2], versions


SCENARIOS = {
    1: ("Базовый цикл склада", warehouse_cycle),
    2: ("Идемпотентность после перезапуска", idempotency),
    3: ("Согласованность таблиц", consistent_tables),
    4: ("События вне порядка", out_of_order),
    5: ("Dead Letter Queue", dead_letter_queue),
    6: ("Отказ одной ноды Cassandra", failover),
    7: ("Мониторинг, lag и алерт", monitoring),
    8: ("Две версии Avro", schema_evolution),
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenarios", nargs="*", type=int, metavar="N")
    args = parser.parse_args()
    for number in args.scenarios:
        if number not in SCENARIOS:
            parser.error(f"неизвестный сценарий {number}, выберите номер от 1 до 8")

    wait_for("consumer готов к проверкам", lambda: status(CONSUMER + "/health") == 200, timeout=300)
    for number in args.scenarios or SCENARIOS:
        name, scenario = SCENARIOS[number]
        print(f"{number}. {name}", flush=True)
        scenario()
        print("   OK", flush=True)


if __name__ == "__main__":
    main()

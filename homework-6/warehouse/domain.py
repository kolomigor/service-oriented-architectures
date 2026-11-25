from collections import defaultdict
from datetime import datetime, timezone
import json
from uuid import UUID

from warehouse.events import InvalidEvent

EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)
MAX_QUANTITY = 2**63 - 1
STOCK_EVENTS = {
    "PRODUCT_RECEIVED", "PRODUCT_SHIPPED", "PRODUCT_MOVED",
    "PRODUCT_RESERVED", "PRODUCT_RELEASED", "INVENTORY_COUNTED",
}


def event_id(event):
    try:
        return UUID(event["event_id"])
    except (KeyError, ValueError, TypeError, AttributeError) as error:
        raise InvalidEvent("event_id must be a UUID") from error


def event_time_us(event):
    try:
        time = datetime.fromisoformat(event["timestamp"])
        if time.utcoffset() is None:
            raise ValueError("timezone required")
        elapsed = time - EPOCH
        return (elapsed.days * 86400 + elapsed.seconds) * 1_000_000 + elapsed.microseconds
    except (KeyError, TypeError, ValueError, OverflowError) as error:
        raise InvalidEvent("timestamp must be ISO 8601 with a timezone") from error


def identifier(event, field):
    value = event.get(field)
    if not isinstance(value, str) or not value.strip():
        raise InvalidEvent(f"{field} is required")
    return value


def quantity(event, field, minimum=1):
    value = event.get(field)
    if type(value) is not int or not minimum <= value <= MAX_QUANTITY:
        description = "positive" if minimum else "non-negative"
        raise InvalidEvent(f"Invalid {field}: {value} (must be a {description} integer)")
    return value


def order_items(event):
    items = event.get("items")
    if not items:
        raise InvalidEvent("items must contain at least one order item")
    return [
        {"product_id": identifier(item, "product_id"), "zone_id": identifier(item, "zone_id"),
         "quantity": quantity(item, "quantity")}
        for item in items
    ]


def stock_changes(event, kind):
    product = identifier(event, "product_id")
    if kind == "PRODUCT_MOVED":
        source = identifier(event, "from_zone_id")
        destination = identifier(event, "to_zone_id")
        if source == destination:
            raise InvalidEvent("Source and destination zones must differ")
        amount = quantity(event, "quantity")
        return {(product, source): (-amount, 0), (product, destination): (amount, 0)}

    zone = identifier(event, "zone_id")
    if kind == "INVENTORY_COUNTED":
        return {(product, zone): (quantity(event, "counted_quantity", minimum=0), 0)}

    amount = quantity(event, "quantity")
    change = {
        "PRODUCT_RECEIVED": (amount, 0),
        "PRODUCT_SHIPPED": (-amount, 0),
        "PRODUCT_RESERVED": (-amount, amount),
        "PRODUCT_RELEASED": (amount, -amount),
    }[kind]
    return {(product, zone): change}


def calculate_mutation(store, event, version, write_timestamp):
    kind = event["event_type"]
    event_us = event_time_us(event)
    order = None
    current_order = None
    if kind in STOCK_EVENTS:
        changes = stock_changes(event, kind)
    elif kind in ("ORDER_CREATED", "ORDER_COMPLETED"):
        order_id = identifier(event, "order_id")
        current_order = store.order(order_id)
        if current_order and event_us <= current_order.last_event_us:
            return None
        if kind == "ORDER_CREATED":
            items = order_items(event)
            order = (order_id, "CREATED", items)
        else:
            if current_order is None:
                raise InvalidEvent(f"Unknown order {order_id}")
            items = json.loads(current_order.items_json)
            order = (order_id, "COMPLETED", items)
        amounts = defaultdict(int)
        for item in items:
            amounts[item["product_id"], item["zone_id"]] += item["quantity"]
        if kind == "ORDER_CREATED":
            changes = {key: (-amount, amount) for key, amount in amounts.items()}
        else:
            changes = {key: (0, -amount) for key, amount in amounts.items()}
    else:
        raise InvalidEvent(f"Unknown event_type {kind}")

    rows = {key: store.inventory(*key) for key in changes}
    if any(row and event_us <= row.last_event_us for row in rows.values()):
        return None

    if kind == "ORDER_CREATED" and current_order:
        raise InvalidEvent(f"Order {order[0]} already exists")
    if kind == "ORDER_COMPLETED" and current_order.status != "CREATED":
        raise InvalidEvent(f"Order {order[0]} is already completed")

    inventory = []
    total_changes = defaultdict(lambda: (0, 0))
    for (product, zone), (available_change, reserved_change) in changes.items():
        previous = rows[product, zone]
        available = previous.available_quantity if previous else 0
        reserved = previous.reserved_quantity if previous else 0
        supplier = previous.supplier_id if previous else None
        if kind == "INVENTORY_COUNTED":
            available_change -= available
        if kind == "PRODUCT_RECEIVED":
            supplier = event.get("supplier_id") if version == 2 else None
        new_available = available + available_change
        new_reserved = reserved + reserved_change
        if not (0 <= new_available <= MAX_QUANTITY and 0 <= new_reserved <= MAX_QUANTITY):
            raise InvalidEvent(f"Invalid balance for {product} in {zone}: {new_available}/{new_reserved}")
        inventory.append((product, zone, new_available, new_reserved, supplier))
        total_available_change, total_reserved_change = total_changes[product]
        total_changes[product] = (total_available_change + available_change,
                                  total_reserved_change + reserved_change)

    totals = []
    for product, (available_change, reserved_change) in total_changes.items():
        previous = store.total(product)
        available = (previous.total_available if previous else 0) + available_change
        reserved = (previous.total_reserved if previous else 0) + reserved_change
        if not (0 <= available <= MAX_QUANTITY and 0 <= reserved <= MAX_QUANTITY):
            raise InvalidEvent(f"Invalid total balance for {product}: {available}/{reserved}")
        totals.append((product, available, reserved))

    return {
        "inventory": inventory, "totals": totals, "order": order,
        "event_us": event_us, "write_timestamp": write_timestamp,
    }

#!/usr/bin/env python3
"""Exercise the running HTTP API; leave the records in PostgreSQL for inspection."""

import concurrent.futures
import datetime as dt
import json
import os
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from decimal import Decimal

BASE = os.getenv("E2E_BASE_URL", os.getenv("BASE_URL", "http://localhost:8080")).rstrip("/")
RATE = float(os.getenv("E2E_RATE_LIMIT_SECONDS", str(float(os.getenv("ORDER_RATE_LIMIT_MINUTES", "1")) * 60)))
SUFFIX = uuid.uuid4().hex[:10]
CATEGORY = "e2e_" + SUFFIX
PASSWORD = "E2ePassword123!"
CHECKS = 0
LOCK = threading.Lock()


def check(condition, message):
    global CHECKS
    if not condition:
        raise AssertionError(message)
    with LOCK:
        CHECKS += 1


def send(method, path, data=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    raw = json.dumps(data, default=lambda v: float(v) if isinstance(v, Decimal) else str(v)).encode() if data is not None else None
    request = urllib.request.Request(BASE + path, data=raw, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        status = response.status
        request_id = response.headers.get("X-Request-Id", "")
    try:
        uuid.UUID(request_id)
    except ValueError as error:
        raise AssertionError(f"{method} {path}: missing or invalid X-Request-Id") from error
    check(bool(request_id), "request ID missing")
    return status, json.loads(body, parse_float=Decimal) if body else None


def expect(method, path, data=None, token=None, status=200, code=None):
    actual, body = send(method, path, data, token)
    actual_code = body.get("error_code") if isinstance(body, dict) else None
    check(actual == status, f"{method} {path}: expected {status}, received {actual} ({actual_code})")
    if code:
        check(actual_code == code, f"{method} {path}: expected {code}, received {actual_code}")
        check(isinstance(body.get("message"), str), "error message missing")
        if code == "VALIDATION_ERROR":
            check(bool(body.get("details", {}).get("fields")), "validation does not identify fields")
    return body


def user(role="USER"):
    email = f"e2e-{SUFFIX}-{uuid.uuid4().hex[:8]}@example.test"
    account = expect("POST", "/auth/register", {"email": email, "password": PASSWORD, "role": role}, status=201)
    pair = expect("POST", "/auth/login", {"email": email, "password": PASSWORD})
    check(account["role"] == role and pair["token_type"] == "Bearer", "registration/login role or token type")
    return {**account, "token": pair["access_token"], "refresh": pair["refresh_token"]}


def product(seller, name="Product", price=100, stock=20, status="ACTIVE"):
    return expect("POST", "/products", {"name": name, "description": "E2E", "price": price, "stock": stock,
                  "category": CATEGORY, "status": status}, seller["token"], 201)


def update_product(actor, item, **changes):
    payload = {key: item[key] for key in ("name", "description", "price", "stock", "category", "status")}
    payload.update(changes)
    return expect("PUT", "/products/" + item["id"], payload, actor["token"])


def stock(item, actor):
    return expect("GET", "/products/" + item["id"], token=actor["token"])["stock"]


def items(*positions):
    return {"items": [{"product_id": p["id"], "quantity": quantity} for p, quantity in positions]}


def order(actor, positions, promo=None, status=201, code=None):
    body = items(*positions)
    if promo:
        body["promo_code"] = promo["code"]
    return expect("POST", "/orders", body, actor["token"], status, code)


def cancel(actor, purchase):
    return expect("POST", "/orders/" + purchase["id"] + "/cancel", token=actor["token"])


def promo(seller, kind="PERCENTAGE", value=20, minimum=0, uses=1, active=True, expired=False):
    now = dt.datetime.now(dt.timezone.utc)
    start, end = (now - dt.timedelta(days=2), now - dt.timedelta(days=1)) if expired else (now - dt.timedelta(days=1), now + dt.timedelta(days=1))
    body = {"code": "E2E_" + uuid.uuid4().hex[:12].upper(), "discount_type": kind, "discount_value": value,
            "min_order_amount": minimum, "max_uses": uses, "active": active,
            "valid_from": start.isoformat(), "valid_until": end.isoformat()}
    return expect("POST", "/promo-codes", body, seller["token"], 201)


def parallel(calls):
    barrier = threading.Barrier(len(calls))
    def request(args):
        barrier.wait(timeout=10)
        return send(*args)
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(calls)) as pool:
        return list(pool.map(request, calls))


def main():
    expect("GET", "/health")
    admin = {"token": expect("POST", "/auth/login", {
        "email": os.getenv("ADMIN_EMAIL", "admin@example.test"),
        "password": os.getenv("ADMIN_PASSWORD", "LocalAdmin123!")})["access_token"]}
    buyer, stranger, seller, seller2 = user(), user(), user("SELLER"), user("SELLER")
    expect("GET", "/products", status=401, code="TOKEN_INVALID")
    expect("GET", "/products", token="invalid.jwt", status=401, code="TOKEN_INVALID")
    expect("POST", "/auth/login", {"email": buyer["email"], "password": "WrongPassword123!"}, status=401, code="AUTH_INVALID_CREDENTIALS")
    expect("POST", "/auth/register", {"email": buyer["email"].upper(), "password": PASSWORD}, status=409, code="USER_ALREADY_EXISTS")
    expect("POST", "/auth/register", {"email": "forbidden@example.test", "password": PASSWORD, "role": "ADMIN"}, status=400, code="VALIDATION_ERROR")
    expect("POST", "/auth/register", {"email": "bytes@example.test", "password": "я" * 37}, status=400, code="VALIDATION_ERROR")
    pair = expect("POST", "/auth/refresh", {"refresh_token": buyer["refresh"]})
    expect("POST", "/auth/refresh", {"refresh_token": buyer["refresh"]}, status=401, code="REFRESH_TOKEN_INVALID")
    expect("POST", "/auth/refresh", {"refresh_token": "invalid"}, status=401, code="REFRESH_TOKEN_INVALID")
    expect("POST", "/auth/refresh", {"refresh_token": buyer["token"]}, status=401, code="REFRESH_TOKEN_INVALID")
    expect("GET", "/products", token=pair["refresh_token"], status=401, code="TOKEN_INVALID")
    buyer["token"] = pair["access_token"]
    expect("POST", "/products", {"name": "Denied", "price": 100, "stock": 1, "category": CATEGORY, "status": "ACTIVE"}, buyer["token"], 403, "ACCESS_DENIED")
    p1, p2, inactive, archived = product(seller, "First"), product(seller, "Second", 50), product(seller, status="INACTIVE"), product(seller, "Archive")
    archived = update_product(seller, archived, description=None)
    check(archived.get("description") is None, "description not cleared")
    expect("PUT", "/products/" + p1["id"], {k: p1[k] for k in ("name", "price", "stock", "category", "status")}, seller2["token"], 403, "ACCESS_DENIED")
    expect("DELETE", "/products/" + p1["id"], token=seller2["token"], status=403, code="ACCESS_DENIED")
    p1 = update_product(admin, p1, description="Administrator edit")
    expect("DELETE", "/products/" + archived["id"], token=seller["token"], status=204)
    archived = expect("GET", "/products/" + archived["id"], token=buyer["token"])
    expect("DELETE", "/products/" + archived["id"], token=seller["token"], status=204)
    check(expect("GET", "/products/" + archived["id"], token=buyer["token"])["updated_at"] == archived["updated_at"], "repeat archive changes dates")
    query = "/products?" + urllib.parse.urlencode({"category": CATEGORY})
    page = expect("GET", query, token=buyer["token"])
    check(page["page"] == 0 and page["size"] == 20 and page["totalElements"] == 4, "default pagination/count")
    filtered = expect("GET", query + "&status=ACTIVE&size=1&page=1", token=buyer["token"])
    check(filtered["totalElements"] == 2 and len(filtered["items"]) == 1 and filtered["page"] == 1, "filtered pagination")
    check(expect("GET", query + "&page=999", token=buyer["token"])["items"] == [], "out-of-range page")
    check(expect("GET", "/products?category=" + CATEGORY.upper(), token=buyer["token"])["totalElements"] == 0, "category matching must be exact")
    expect("GET", "/products/not-a-uuid", token=buyer["token"], status=400, code="VALIDATION_ERROR")
    expect("GET", "/products/" + str(uuid.uuid4()), token=buyer["token"], status=404, code="PRODUCT_NOT_FOUND")
    for path in ("/products?status=UNKNOWN", "/products?page=-1", "/products?size=0"):
        expect("GET", path, token=buyer["token"], status=400, code="VALIDATION_ERROR")
    base = {"name": "Validation", "price": 100, "stock": 1, "category": CATEGORY, "status": "ACTIVE"}
    for changes in ({"price": -1}, {"price": 1.001}, {"stock": -1}, {"name": ""}, {"status": "UNKNOWN"}, {"description": "x" * 4001}):
        expect("POST", "/products", {**base, **changes}, seller["token"], 400, "VALIDATION_ERROR")
    for bad in ({"items": []}, {"items": [{"product_id": str(uuid.uuid4()), "quantity": 1}] * 51}, items((p1, 0)), items((p1, 1000)), {**items((p1, 1)), "promo_code": "bad!"}, items((p1, 1), (p1, 1)), {"items": [{"product_id": p1["id"], "quantity": 1}, {"product_id": p1["id"].upper(), "quantity": 1}]}):
        expect("POST", "/orders", bad, buyer["token"], 400, "VALIDATION_ERROR")
    expect("POST", "/orders", items((p1, 1)), seller["token"], 403, "ACCESS_DENIED")
    expect("GET", "/orders/" + str(uuid.uuid4()), token=buyer["token"], status=404, code="ORDER_NOT_FOUND")
    order(stranger, [({"id": str(uuid.uuid4())}, 1)], status=404, code="PRODUCT_NOT_FOUND")
    order(stranger, [(inactive, 1)], status=409, code="PRODUCT_INACTIVE")
    shortage = order(stranger, [(p1, 999), (p2, 999)], status=409, code="INSUFFICIENT_STOCK")
    check(len(shortage["details"]["items"]) == 2 and stock(p1, buyer) == 20 and stock(p2, buyer) == 20, "shortage must aggregate and roll back")
    order(stranger, [(p1, 1)], {"code": "MISSING_" + SUFFIX.upper()}, 422, "PROMO_CODE_INVALID")
    for invalid in (promo(seller, active=False), promo(seller, expired=True)):
        order(stranger, [(p1, 1)], invalid, 422, "PROMO_CODE_INVALID")
    order(stranger, [(p1, 1)], promo(seller, minimum=500), 422, "PROMO_CODE_MIN_AMOUNT")
    check(stock(p1, buyer) == 20, "promo rejection reserves inventory")
    purchase = order(buyer, [(p1, 2), (p2, 2)])
    check(purchase["total_amount"] == Decimal("300") and stock(p1, buyer) == 18, "initial reservation/total")
    expect("PUT", "/products/" + p1["id"], {**{k: p1[k] for k in ("name", "price", "category", "status")}, "stock": 2147483647}, seller["token"], 400, "VALIDATION_ERROR")
    check(stock(p1, buyer) == 18, "rejected stock overflow changes inventory")
    order(buyer, [(p1, 1)], status=429, code="ORDER_LIMIT_EXCEEDED")
    time.sleep(RATE + 0.15)
    order(buyer, [(p1, 1)], status=409, code="ORDER_HAS_ACTIVE")
    for method, tail, body in (("GET", "", None), ("PUT", "", items((p1, 1))), ("POST", "/cancel", None)):
        expect(method, "/orders/" + purchase["id"] + tail, body, stranger["token"], 403, "ORDER_OWNERSHIP_VIOLATION")
    for method, tail, body in (("GET", "", None), ("PUT", "", items((p1, 1))), ("POST", "/cancel", None)):
        expect(method, "/orders/" + purchase["id"] + tail, body, seller["token"], 403, "ACCESS_DENIED")
    expect("GET", "/orders/" + purchase["id"], token=admin["token"])
    p1 = update_product(seller, expect("GET", "/products/" + p1["id"], token=seller["token"]), price=150)
    snapshots = expect("GET", "/orders/" + purchase["id"], token=buyer["token"])["items"]
    check({row["product_id"]: row["price_at_order"] for row in snapshots} == {p1["id"]: Decimal("100"), p2["id"]: Decimal("50")}, "price snapshot changed")
    purchase = expect("PUT", "/orders/" + purchase["id"], items((p1, 3), (p2, 1)), admin["token"])
    check(purchase["total_amount"] == Decimal("350") and stock(p1, buyer) == 17 and stock(p2, buyer) == 19, "update reservations or price snapshots")
    expect("PUT", "/orders/" + purchase["id"], items((p1, 1)), buyer["token"], 429, "ORDER_LIMIT_EXCEEDED")
    time.sleep(RATE + 0.15)
    expect("PUT", "/orders/" + purchase["id"], items((p1, 999), (p2, 1)), buyer["token"], 409, "INSUFFICIENT_STOCK")
    check(stock(p1, buyer) == 17 and stock(p2, buyer) == 19, "failed update did not roll back")
    check(cancel(buyer, purchase)["status"] == "CANCELED" and stock(p1, buyer) == 20 and stock(p2, buyer) == 20, "cancel did not restore inventory")
    expect("POST", "/orders/" + purchase["id"] + "/cancel", token=buyer["token"], status=409, code="INVALID_STATE_TRANSITION")
    discount_product = product(seller, "Discount", 100)
    cent_product, cent_user, cent_coupon = product(seller, "One cent", 0.01), user(), promo(seller, value=70)
    cent_order = order(cent_user, [(cent_product, 1)], cent_coupon)
    check(cent_order["discount_amount"] == Decimal("0") and cent_order["total_amount"] == Decimal("0.01"), "rounded percentage discount exceeds 70 percent")
    cancel(cent_user, cent_order)
    for kind, value, discount, total in (("PERCENTAGE", 20, 20, 80), ("PERCENTAGE", 71, 0, 100), ("FIXED_AMOUNT", 1000, 100, 0)):
        customer, coupon = user(), promo(seller, kind, value)
        discounted = order(customer, [(discount_product, 1)], coupon)
        check(discounted["discount_amount"] == Decimal(discount) and discounted["total_amount"] == Decimal(total), "discount rule incorrect")
        order(stranger, [(discount_product, 1)], coupon, 422, "PROMO_CODE_INVALID")
        cancel(customer, discounted)
        next_customer = user()
        cancel(next_customer, order(next_customer, [(discount_product, 1)], coupon))
    customer, coupon = user(), promo(seller, minimum=150)
    expect("POST", "/promo-codes", {k: coupon[k] for k in ("code", "discount_type", "discount_value", "min_order_amount", "max_uses", "active", "valid_from", "valid_until")}, buyer["token"], 403, "ACCESS_DENIED")
    discounted = order(customer, [(discount_product, 2)], coupon)
    stripped = expect("PUT", "/orders/" + discounted["id"], items((discount_product, 1)), customer["token"])
    check(stripped["promo_code_id"] is None and stripped["discount_amount"] == 0 and stripped["total_amount"] == 100, "minimum reduction did not remove promo")
    reuse = user()
    cancel(reuse, order(reuse, [(discount_product, 2)], coupon))
    cancel(customer, stripped)
    state_user = user()
    state_order = order(state_user, [(discount_product, 1)])
    endpoint = "/orders/" + state_order["id"] + "/status"
    expect("POST", endpoint, {"status": "PAID"}, admin["token"], 409, "INVALID_STATE_TRANSITION")
    expect("POST", endpoint, {"status": "PAYMENT_PENDING"}, state_user["token"], 403, "ACCESS_DENIED")
    for state in ("PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED"):
        check(expect("POST", endpoint, {"status": state}, admin["token"])["status"] == state, "state transition")
        if state == "PAYMENT_PENDING":
            expect("PUT", "/orders/" + state_order["id"], items((discount_product, 1)), state_user["token"], 409, "INVALID_STATE_TRANSITION")
        if state == "PAID":
            expect("POST", "/orders/" + state_order["id"] + "/cancel", token=state_user["token"], status=409, code="INVALID_STATE_TRANSITION")
    pending_user = user()
    before_pending = stock(discount_product, buyer)
    pending_order = order(pending_user, [(discount_product, 1)])
    expect("POST", "/orders/" + pending_order["id"] + "/status", {"status": "PAYMENT_PENDING"}, admin["token"])
    check(cancel(pending_user, pending_order)["status"] == "CANCELED" and stock(discount_product, buyer) == before_pending, "pending cancellation did not restore stock")
    last = product(seller, "Last unit", stock=1)
    a, b = user(), user()
    results = parallel([("POST", "/orders", items((last, 1)), person["token"]) for person in (a, b)])
    check(sorted(r[0] for r in results) == [201, 409], "last unit race must have one winner")
    check(next(r[1] for r in results if r[0] == 409)["error_code"] == "INSUFFICIENT_STOCK" and stock(last, buyer) == 0, "last unit race inventory")
    racer, race_product = user(), product(seller, "One active order", stock=5)
    results = parallel([("POST", "/orders", items((race_product, 1)), racer["token"])] * 2)
    check(sorted(r[0] for r in results) == [201, 429], "same user race must serialize rate check")
    check(next(r[1] for r in results if r[0] == 429)["error_code"] == "ORDER_LIMIT_EXCEEDED" and stock(race_product, buyer) == 4, "same user race rollback")
    coupon, pa, pb, a, b = promo(seller), product(seller, "Promo A"), product(seller, "Promo B"), user(), user()
    results = parallel([("POST", "/orders", {**items((p, 1)), "promo_code": coupon["code"]}, person["token"]) for p, person in ((pa, a), (pb, b))])
    check(sorted(r[0] for r in results) == [201, 422], "last promo slot race must have one winner")
    check(next(r[1] for r in results if r[0] == 422)["error_code"] == "PROMO_CODE_INVALID" and sorted((stock(pa, buyer), stock(pb, buyer))) == [19, 20], "promo race rollback")
    refresh_user = user()
    results = parallel([("POST", "/auth/refresh", {"refresh_token": refresh_user["refresh"]}, None)] * 2)
    check(sorted(r[0] for r in results) == [200, 401], "refresh rotation race must have one winner")
    check(next(r[1] for r in results if r[0] == 401)["error_code"] == "REFRESH_TOKEN_INVALID", "refresh reuse error")
    print(f"E2E passed: {CHECKS} checks. Dataset category: {CATEGORY}")
    print(f"Database inspection IDs: canceled_order={purchase['id']} completed_order={state_order['id']} last_unit_product={last['id']} promo_race_code={coupon['code']}")


if __name__ == "__main__":
    main()

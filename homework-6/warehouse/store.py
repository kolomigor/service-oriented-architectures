import json

from cassandra import ConsistencyLevel
from cassandra.cluster import Cluster, ExecutionProfile, EXEC_PROFILE_DEFAULT
from cassandra.policies import DCAwareRoundRobinPolicy, FallthroughRetryPolicy
from cassandra.query import BatchStatement

from warehouse.config import CASSANDRA_DC, CASSANDRA_HOSTS, KEYSPACE


def cassandra_cluster():
    profile = ExecutionProfile(
        load_balancing_policy=DCAwareRoundRobinPolicy(local_dc=CASSANDRA_DC),
        consistency_level=ConsistencyLevel.QUORUM,
        retry_policy=FallthroughRetryPolicy(),
        request_timeout=5,
    )
    return Cluster(CASSANDRA_HOSTS, protocol_version=5, execution_profiles={EXEC_PROFILE_DEFAULT: profile})


class Store:
    def __init__(self, session):
        self.session = session
        self.get_inventory = session.prepare(
            "SELECT * FROM inventory_by_product_zone WHERE product_id = ? AND zone_id = ?"
        )
        self.get_total = session.prepare("SELECT * FROM inventory_by_product WHERE product_id = ?")
        self.get_order = session.prepare("SELECT * FROM orders_by_id WHERE order_id = ?")
        self.get_event = session.prepare(
            "SELECT applied, mutation_json FROM processed_events WHERE event_id = ?"
        )
        self.save_event = session.prepare(
            "INSERT INTO processed_events (event_id, mutation_json) VALUES (?, ?)"
        )
        self.finish_event = session.prepare("UPDATE processed_events SET applied = true WHERE event_id = ?")
        self.write_zone_for_product = session.prepare(
            "INSERT INTO inventory_by_product_zone "
            "(product_id, zone_id, available_quantity, reserved_quantity, supplier_id, last_event_us) "
            "VALUES (?, ?, ?, ?, ?, ?) USING TIMESTAMP ?"
        )
        self.write_product_for_zone = session.prepare(
            "INSERT INTO inventory_by_zone "
            "(zone_id, product_id, available_quantity, reserved_quantity, supplier_id) "
            "VALUES (?, ?, ?, ?, ?) USING TIMESTAMP ?"
        )
        self.write_total = session.prepare(
            "INSERT INTO inventory_by_product (product_id, total_available, total_reserved) "
            "VALUES (?, ?, ?) USING TIMESTAMP ?"
        )
        self.write_order = session.prepare(
            "INSERT INTO orders_by_id (order_id, status, items_json, last_event_us) "
            "VALUES (?, ?, ?, ?) USING TIMESTAMP ?"
        )

    @classmethod
    def connect(cls, cluster):
        return cls(cluster.connect(KEYSPACE))

    def inventory(self, product_id, zone_id):
        return self.session.execute(self.get_inventory, (product_id, zone_id)).one()

    def total(self, product_id):
        return self.session.execute(self.get_total, (product_id,)).one()

    def order(self, order_id):
        return self.session.execute(self.get_order, (order_id,)).one()

    def event(self, event_id, timeout=None):
        options = {} if timeout is None else {"timeout": timeout}
        return self.session.execute(self.get_event, (event_id,), **options).one()

    def save_mutation(self, event_id, mutation):
        self.session.execute(self.save_event, (event_id, json.dumps(mutation)))

    def mark_applied(self, event_id):
        self.session.execute(self.finish_event, (event_id,))

    def apply_mutation(self, mutation):
        timestamp = mutation["write_timestamp"]
        batch = BatchStatement(consistency_level=ConsistencyLevel.QUORUM)
        for product, zone, available, reserved, supplier in mutation["inventory"]:
            batch.add(self.write_zone_for_product, (
                product, zone, available, reserved, supplier, mutation["event_us"], timestamp,
            ))
            batch.add(self.write_product_for_zone, (zone, product, available, reserved, supplier, timestamp))
        for product, available, reserved in mutation["totals"]:
            batch.add(self.write_total, (product, available, reserved, timestamp))
        if order := mutation["order"]:
            order_id, status, items = order
            batch.add(self.write_order, (order_id, status, json.dumps(items), mutation["event_us"], timestamp))
        self.session.execute(batch)

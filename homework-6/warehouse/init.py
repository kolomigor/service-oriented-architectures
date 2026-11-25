import logging
from pathlib import Path
import time

from cassandra import OperationTimedOut, Unavailable
from cassandra.cluster import NoHostAvailable
from confluent_kafka import KafkaError, KafkaException
from confluent_kafka.admin import AdminClient, NewTopic

from warehouse.config import CASSANDRA_DC, DLQ_TOPIC, KAFKA_BOOTSTRAP, SCHEMA_REGISTRY_URL
from warehouse.events import EVENT_TOPIC, Registry, RegistryUnavailable
from warehouse.store import cassandra_cluster


def initialize_kafka():
    admin = AdminClient({"bootstrap.servers": KAFKA_BOOTSTRAP})
    for name, future in admin.create_topics(
        [NewTopic(name, num_partitions=1, replication_factor=1)
         for name in (EVENT_TOPIC, DLQ_TOPIC)],
        request_timeout=10,
    ).items():
        try:
            future.result()
        except KafkaException as error:
            if error.args[0].code() != KafkaError.TOPIC_ALREADY_EXISTS:
                raise
        topic = admin.list_topics(name, timeout=5).topics[name]
        if topic.error:
            raise KafkaException(topic.error)
        if len(topic.partitions) != 1:
            raise RuntimeError(f"{name} must have exactly one partition")
    Registry(SCHEMA_REGISTRY_URL).register_schemas()


def initialize_cassandra():
    migrations = Path(__file__).resolve().parents[1] / "migrations"
    with cassandra_cluster() as cluster:
        session = cluster.connect()
        session.execute(
            "CREATE KEYSPACE IF NOT EXISTS warehouse "
            f"WITH replication = {{'class': 'NetworkTopologyStrategy', '{CASSANDRA_DC}': 3}}"
        )
        for migration in sorted(migrations.glob("*.cql")):
            for statement in migration.read_text().split(";"):
                if statement.strip():
                    session.execute(statement.strip())
            logging.info("Applied %s", migration.name)


def main():
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    for step in (initialize_kafka, initialize_cassandra):
        for attempt in range(30):
            try:
                step()
                break
            except (KafkaException, RegistryUnavailable, NoHostAvailable, OperationTimedOut, Unavailable) as error:
                if attempt == 29:
                    raise
                logging.info("Waiting for %s: %s", step.__name__, error)
                time.sleep(2)
    logging.info("Warehouse initialized")


if __name__ == "__main__":
    main()

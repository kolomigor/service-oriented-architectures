import os

KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "kafka:9092")
SCHEMA_REGISTRY_URL = os.getenv("SCHEMA_REGISTRY_URL", "http://schema-registry:8081")
GROUP = "warehouse-state-consumer"
DLQ_TOPIC = "warehouse-events-dlq"
KEYSPACE = "warehouse"
CASSANDRA_HOSTS = os.getenv("CASSANDRA_HOSTS", "cassandra-1,cassandra-2,cassandra-3").split(",")
CASSANDRA_DC = os.getenv("CASSANDRA_DC", "datacenter1")

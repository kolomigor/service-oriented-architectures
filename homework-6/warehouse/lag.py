import logging
import os
import time

from confluent_kafka import ConsumerGroupTopicPartitions, KafkaException, TopicPartition
from confluent_kafka.admin import AdminClient, OffsetSpec
from prometheus_client import CollectorRegistry, start_http_server
from prometheus_client.core import GaugeMetricFamily

from warehouse.config import GROUP, KAFKA_BOOTSTRAP
from warehouse.events import EVENT_TOPIC


class KafkaLag:
    def __init__(self):
        self.admin = AdminClient({"bootstrap.servers": KAFKA_BOOTSTRAP})

    def collect(self):
        metric = GaugeMetricFamily(
            "consumer_lag", "Kafka log end offset minus the group's committed offset",
            labels=["topic", "partition"],
        )
        try:
            topic = self.admin.list_topics(EVENT_TOPIC, timeout=2).topics[EVENT_TOPIC]
            if topic.error:
                raise KafkaException(topic.error)
            partitions = [TopicPartition(EVENT_TOPIC, number) for number in topic.partitions]
            committed = self.admin.list_consumer_group_offsets(
                [ConsumerGroupTopicPartitions(GROUP, partitions)], request_timeout=2,
            )[GROUP].result().topic_partitions
            ends = self.admin.list_offsets(
                {partition: OffsetSpec.latest() for partition in partitions}, request_timeout=2,
            )
            starts = self.admin.list_offsets(
                {partition: OffsetSpec.earliest() for partition in partitions}, request_timeout=2,
            )
            for partition in committed:
                if partition.error:
                    raise KafkaException(partition.error)
                begin = starts[partition].result().offset
                end = ends[partition].result().offset
                lag = max(0, end - max(partition.offset, begin))
                metric.add_metric([EVENT_TOPIC, str(partition.partition)], lag)
        except (KafkaException, KeyError) as error:
            logging.warning("Cannot read consumer lag: %s", error)
            return
        yield metric


def main():
    registry = CollectorRegistry()
    registry.register(KafkaLag())
    start_http_server(int(os.getenv("HTTP_PORT", "8081")), registry=registry)
    while True:
        time.sleep(60)


if __name__ == "__main__":
    main()

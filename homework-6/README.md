# Smart Warehouse

Склад из ДЗ №6. Producer отправляет события в Kafka, consumer пересчитывает остатки и заказы, Cassandra хранит результат. Условия и восемь сценариев проверки описаны в [TASK.md](TASK.md).

## Запуск

Выполните команды из папки `homework-6`.

```sh
docker compose up -d --build
docker compose ps
curl -fsS http://localhost:8080/health
```

Первый запуск ждёт, пока три ноды Cassandra войдут в кластер. Сервис `init` создаёт Kafka-топики, регистрирует Avro-схемы и применяет CQL-миграции. Статус `Exited (0)` у `init` означает, что инициализация завершилась. Kafka и Cassandra используют постоянные тома.

| Где | Что смотреть |
| --- | --- |
| `http://localhost:8080/health` | `200`, если доступны Kafka и кворум Cassandra, иначе `503` |
| `http://localhost:8080/metrics` | Lag по партициям, число событий по типам, длительность обработки и ошибки записи |
| `http://localhost:3000` | Дашборд **Warehouse** с lag, скоростью обработки и ошибками записи |
| `http://localhost:9090/alerts` | Алерт `WarehouseConsumerLag` |
| `http://localhost:8081/subjects` | Схемы в Schema Registry |

Если порт занят, задайте `CONSUMER_PORT`, `GRAFANA_PORT`, `PROMETHEUS_PORT` или `SCHEMA_REGISTRY_PORT` перед запуском. Скрипт проверки читает те же переменные.

## Отправить одно событие

```sh
docker compose run --rm -T --no-deps producer - <<'JSON'
{"event_type":"PRODUCT_RECEIVED","product_id":"DEMO-001","zone_id":"A","quantity":100}
JSON
docker compose logs --tail=15 consumer
docker compose exec cassandra-1 cqlsh
```

Producer заполняет `event_id` и UTC `timestamp`, если их нет во входном JSON. Он выводит отправленное событие. Для проверки дублей можно передать этот JSON повторно с тем же `event_id`.

```sql
CONSISTENCY QUORUM;
SELECT * FROM warehouse.inventory_by_product_zone WHERE product_id = 'DEMO-001' AND zone_id = 'A';
SELECT * FROM warehouse.inventory_by_product WHERE product_id = 'DEMO-001';
SELECT * FROM warehouse.inventory_by_zone WHERE zone_id = 'A';
```

## Таблицы и порядок обработки

| Таблица | Ключ и назначение |
| --- | --- |
| `inventory_by_product_zone` | Partition key `product_id`, clustering key `zone_id`. Точный остаток товара в зоне. |
| `inventory_by_product` | Partition key `product_id`. Итог по всем зонам одной строкой. |
| `inventory_by_zone` | Partition key `zone_id`, clustering key `product_id`. Список товаров в зоне одним запросом. |
| `orders_by_id` | Partition key `order_id`. Статус и позиции заказа, по которым списывается резерв. |
| `processed_events` | Partition key `event_id`. Итоговые значения для безопасного повтора операции и отметка о её завершении. |

Одна партиция Kafka и один экземпляр consumer задают общий порядок для операций, затрагивающих несколько зон или товаров. После декодирования consumer сверяет `event_id` и время события. Время сравнивается с последним применённым временем для каждой затронутой пары «товар и зона» и для заказа, если он есть. Если хотя бы по одному ключу событие старое или время совпадает, consumer пропускает всё событие.

Для нового события consumer сначала сохраняет в Cassandra рассчитанные абсолютные значения. Затем он записывает их во все нужные таблицы одним **logged batch** и отмечает событие обработанным. Только после этого коммитит следующий offset. При тайм-ауте или рестарте сохранённая операция повторяется с теми же значениями и тем же CQL timestamp. CQL timestamp определяется offset единственной партиции и задаёт порядок записей Cassandra. Время складского события используется отдельно для поиска опоздавших операций. Kafka и Cassandra при демонстрации сохраняют свои тома вместе.

Logged batch позволяет Cassandra довести записи до завершения. Пока он выполняется, чтение разных партиций может увидеть разные стадии обновления. Приложение читает и пишет с `QUORUM`. При RF=3 достаточно двух реплик, а кворумы чтения и записи пересекаются, поэтому расчёт следующего остатка опирается на подтверждённую запись.

Ошибочные события публикуются в `warehouse-events-dlq` вместе с исходным сообщением и причиной ошибки. Consumer коммитит offset, когда Kafka подтверждает публикацию в DLQ. При временных сбоях consumer повторяет текущую операцию с задержкой. Отдельный exporter продолжает собирать lag из Kafka, пока consumer выключен.

## Выпустить новую версию Avro

1. Добавьте версию `ProductReceived` с тем же именем Avro-записи и subject `warehouse-events-ProductReceived`. В V2 поле `supplier_id` имеет тип `["null", "string"]` и `default: null`. При совместимости `BACKWARD` новая схема может прочитать записи старой версии.
2. Зарегистрируйте миграцию для нового поля. Для V2 её пример — [002_supplier.cql](migrations/002_supplier.cql). Добавьте схему в список регистрации в [events.py](warehouse/events.py). На старте `init` задаёт режим `BACKWARD` и отправляет версии в Schema Registry, который проверяет совместимость.
3. Обновите consumer. Он определяет версию по схеме записи из Registry, сохраняет `supplier_id` у V2 и записывает `null` у V1.
4. Соберите и запустите стенд. Отправьте приёмку с `--version 2` и полем `supplier_id`. Затем отправьте ещё одну приёмку по V1, чтобы проверить, что обе версии читаются из одного топика.

```sh
curl -fsS http://localhost:8081/subjects/warehouse-events-ProductReceived/versions
python3 tests/e2e.py 8
```

## Проверить все пункты

```sh
python3 tests/e2e.py
```

Скрипту нужны Python 3 и работающий Compose. Номера сценариев соответствуют списку E2E в задании, например `python3 tests/e2e.py 1 2` запускает базовый цикл и проверку дублей. Для каждого прогона используются новые тестовые SKU. Сценарии 6 и 7 останавливают одну ноду Cassandra и consumer и затем возвращают их в работу.

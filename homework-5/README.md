# Домашнее задание №5. Аналитика онлайн-кинотеатра

## Запуск

Запустите стек из каталога `homework-5`.

```sh
docker-compose up
```

Compose создаёт топик и таблицы, регистрирует схему Avro и загружает дашборд. Продюсер при старте отправляет демонстрационные события за предыдущие дни. После первого пересчёта с этими событиями дашборд заполнится. Для запуска без генератора задайте `DEMO_GENERATOR=false`.

| Что открыть | Адрес | Доступ |
| --- | --- | --- |
| Grafana | [Online Cinema](http://localhost:3000/d/cinema-analytics/online-cinema) | `admin` / `cinema-local` |
| S3 | [RustFS Console](http://localhost:9001/rustfs/console/index.html) | `cinema` / `cinema-local` |
| Schema Registry | [Версии схемы](http://localhost:8081/subjects/movie-events-value/versions) | локальный HTTP |

## Проверки

```sh
docker-compose run --rm --build integration-test
```

Тест отправляет событие в `POST /events`, ждёт запись в ClickHouse и сравнивает все восемь полей, включая время с миллисекундами. Отдельный случай проверяет ошибку валидации. У каждого запуска свой UUID, а дата тестового события вне окна автоматического пересчёта.

### Ручная демонстрация

Передайте продюсеру одно событие. `201` означает, что Kafka подтвердила запись. Для нового события укажите свой UUID и время в UTC.

```sh
curl -fsS http://localhost:8000/events -H 'Content-Type: application/json' -d '{
  "event_id":"bf4416b2-3cc6-4ffa-845b-ae1f828594f2",
  "user_id":"defense-user",
  "movie_id":"Orbit",
  "event_type":"VIEW_STARTED",
  "timestamp":"2026-11-20T18:00:00Z",
  "device_type":"TV",
  "session_id":"defense-session",
  "progress_seconds":0
}'
```

Дождитесь появления события в ClickHouse или выберите дату, за которую генератор уже загрузил данные. Подставьте эту дату в обе команды.

```sh
curl -fsS -X POST http://localhost:8001/aggregate/2026-11-20
curl -fsS -X POST http://localhost:8001/export/2026-11-20
```

Откройте `movie-analytics/daily/2026-11-20/aggregates.json` в консоли RustFS. Повторный экспорт пишет по тому же ключу. Сервис по умолчанию проверяет закрытые дни каждые 60 секунд. Интервал задаёт `AGGREGATION_INTERVAL_SECONDS`.

### Проверка потери одного брокера

Сначала поднимите стек и выполните интеграционный тест. Затем остановите один брокер, повторите тест на уже запущенном стеке и верните брокер.

```sh
docker-compose stop kafka-1
docker-compose run --rm --no-deps integration-test
docker-compose start kafka-1
```

`--no-deps` оставляет брокер остановленным во время теста. Два оставшихся узла составляют большинство контроллеров KRaft. Каждая партиция имеет три реплики и требует минимум одну реплику в ISR для записи.

## Где смотреть код

| Пункты | Файлы |
| --- | --- |
| 1–2. Схема, продюсер и генератор | [schemas/movie-event.avsc](schemas/movie-event.avsc), [producer/app.py](producer/app.py), [producer/demo.py](producer/demo.py) |
| 3–4. Приём данных и тест | [clickhouse/ingestion.sql](clickhouse/ingestion.sql), [tests/integration.py](tests/integration.py) |
| 5. Метрики и сохранение | [analytics/app.py](analytics/app.py), [analytics/sql/](analytics/sql/) |
| 6. Grafana | [grafana/dashboards/cinema.json](grafana/dashboards/cinema.json) |
| 7. JSON в S3 | Функции `export` и `schedule` в [analytics/app.py](analytics/app.py) |
| 8. Кластер и health checks | [compose.yaml](compose.yaml) |

[Текст задания](TASK.md)

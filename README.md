# Service-Oriented Architectures

Домашние работы по сервис-ориентированным архитектурам.

## Домашняя работа 1 — архитектура маркетплейса

- [Архитектура, принятые решения и подробная инструкция](homework-1/README.md)
- [Условие задания](homework-1/TASK.md)
- [C4 Container diagram](homework-1/c4_container_diagram.svg)
- [Статус подготовки к сдаче](homework-1/TODO.md)

Требуется Docker с Compose v2 или новее и свободные порты 8080 и 50052.

```bash
git clone https://github.com/kolomigor/service-oriented-architectures.git
cd service-oriented-architectures/homework-1
docker compose up --build -d --wait
curl -i http://localhost:8080/health
```

Ожидается `HTTP/1.1 200 OK` с телом `ok`. Для остановки из той же папки:

```bash
docker compose down
```

Каждая домашняя работа находится в своей папке. Команды Go, генерации
protobuf и Docker Compose для первой работы выполняются из `homework-1/`.

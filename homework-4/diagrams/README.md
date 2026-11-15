# Диаграммы

Готовые SVG открываются в браузере и встроены в RFC. Файлы `.puml` — исходники PlantUML; менять нужно их, затем повторить рендер.

| Вариант | Архитектура C4 Container | Успешная доставка | Переключение на SMS |
|---|---|---|---|
| PostgreSQL + workers | [a-container.svg](a-container.svg) | [a-success.svg](a-success.svg) | [a-failover.svg](a-failover.svg) |
| RabbitMQ + PostgreSQL | [b-container.svg](b-container.svg) | [b-success.svg](b-success.svg) | [b-failover.svg](b-failover.svg) |

Для повторной генерации нужен Java 8 или новее и [PlantUML 1.2025.10](https://repo.maven.apache.org/maven2/net/sourceforge/plantuml/plantuml/1.2025.10/). Команды из корня репозитория:

```bash
curl -L --fail \
  https://repo.maven.apache.org/maven2/net/sourceforge/plantuml/plantuml/1.2025.10/plantuml-1.2025.10.jar \
  -o /tmp/hw4-plantuml-1.2025.10.jar
java -Djava.awt.headless=true -jar /tmp/hw4-plantuml-1.2025.10.jar \
  -charset UTF-8 -tsvg homework-4/diagrams/*.puml
```

Рендер выполняется локально. C4 использует библиотеку, встроенную в PlantUML, и движок Smetana; отдельная установка Graphviz не нужна.

CREATE TABLE IF NOT EXISTS cinema.daily_metrics
(
    metric_date Date32,
    metric_name LowCardinality(String),
    dimension String,
    value Nullable(Float64),
    computed_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY metric_date
ORDER BY (metric_name, dimension);

CREATE TABLE IF NOT EXISTS cinema.daily_metrics_stage AS cinema.daily_metrics;

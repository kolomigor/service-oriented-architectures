SELECT
    count() AS events_processed,
    uniqExact(user_id) AS dau,
    countIf(event_type = 'VIEW_STARTED') AS started,
    countIf(event_type = 'VIEW_FINISHED') AS finished,
    avgIf(progress_seconds, event_type = 'VIEW_FINISHED') AS avg_watch_seconds
FROM cinema.raw_events FINAL
WHERE timestamp >= toDateTime64({metric_date:Date32}, 3, 'UTC')
  AND timestamp < toDateTime64(addDays({metric_date:Date32}, 1), 3, 'UTC')

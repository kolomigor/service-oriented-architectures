SELECT movie_id, count() AS views
FROM cinema.raw_events FINAL
WHERE timestamp >= toDateTime64({metric_date:Date32}, 3, 'UTC')
  AND timestamp < toDateTime64(addDays({metric_date:Date32}, 1), 3, 'UTC')
  AND event_type = 'VIEW_STARTED'
GROUP BY movie_id
ORDER BY views DESC, movie_id
LIMIT 10

WITH
    {metric_date:Date32} AS cohort_date,
    least(addDays(cohort_date, 7), {observed_through:Date32}) AS last_day,
    cohort AS
    (
        SELECT user_id
        FROM cinema.raw_events FINAL
        WHERE event_type = 'VIEW_STARTED'
          AND timestamp < toDateTime64(addDays(cohort_date, 1), 3, 'UTC')
        GROUP BY user_id
        HAVING min(toDate32(timestamp)) = cohort_date
    )
SELECT
    dateDiff('day', cohort_date, toDate32(timestamp)) AS day_number,
    uniqExact(user_id) AS users
FROM cinema.raw_events FINAL
WHERE timestamp >= toDateTime64(cohort_date, 3, 'UTC')
  AND timestamp < toDateTime64(addDays(last_day, 1), 3, 'UTC')
  AND user_id IN (SELECT user_id FROM cohort)
GROUP BY day_number

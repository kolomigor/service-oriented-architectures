from datetime import UTC, date, datetime, time, timedelta
from uuid import NAMESPACE_URL, uuid5


def demo_events(today: date):
    """Generate ten days ending yesterday, with stable IDs on repeat runs."""
    for days_ago in range(10, 0, -1):
        cohort_date = today - timedelta(days=days_ago)
        for viewer in range(6):
            user_id = f"demo-{cohort_date}-{viewer}"
            for day in range(min(7, days_ago - 1) + 1):
                return_count = max(1, 6 - day - cohort_date.toordinal() % 2)
                if day > 0 and (viewer + day) % 6 >= return_count:
                    continue

                watch_date = cohort_date + timedelta(days=day)
                start = datetime.combine(watch_date, time(12 + viewer), tzinfo=UTC)
                session_id = str(uuid5(NAMESPACE_URL, f"cinema/{user_id}/{day}"))
                movie_id = ("Orbit", "Lighthouse", "Night Train")[(viewer + day) % 3]
                device_type = ("TV", "MOBILE", "TABLET", "DESKTOP")[viewer % 4]
                steps = [
                    ("SEARCHED", -60, 0),
                    ("VIEW_STARTED", 0, 0),
                    ("VIEW_PAUSED", 900, 900),
                    ("VIEW_RESUMED", 990, 920),
                ]
                if (viewer + day) % 4 != 0:
                    duration = 5400 + 120 * viewer
                    steps.append(("VIEW_FINISHED", duration + 90, duration))
                    if viewer % 2 == 0:
                        steps.append(("LIKED", duration + 120, 0))

                for event_type, seconds_after_start, progress in steps:
                    yield {
                        "event_id": str(uuid5(NAMESPACE_URL, f"cinema/{session_id}/{event_type}")),
                        "user_id": user_id,
                        "movie_id": movie_id,
                        "event_type": event_type,
                        "timestamp": (start + timedelta(seconds=seconds_after_start)).isoformat(),
                        "device_type": device_type,
                        "session_id": session_id,
                        "progress_seconds": progress,
                    }

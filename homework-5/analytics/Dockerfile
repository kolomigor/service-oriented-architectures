FROM python:3.12-slim

WORKDIR /app
ENV PYTHONUNBUFFERED=1

COPY analytics/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY analytics/app.py .
COPY analytics/sql sql

CMD ["python", "app.py"]

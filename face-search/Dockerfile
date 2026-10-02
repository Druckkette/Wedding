FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY service.py .

RUN useradd --system --uid 10001 --no-create-home face-search
USER 10001:10001

EXPOSE 8090
CMD ["uvicorn", "service:app", "--host", "0.0.0.0", "--port", "8090", "--workers", "1"]

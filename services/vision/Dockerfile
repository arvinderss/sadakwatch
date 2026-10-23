FROM python:3.12-slim
WORKDIR /app
ARG WITH_YOLO=0
COPY requirements.txt requirements-yolo.txt ./
RUN pip install --no-cache-dir -r requirements.txt && \
    if [ "$WITH_YOLO" = "1" ]; then pip install --no-cache-dir -r requirements-yolo.txt; fi
COPY app.py .
EXPOSE 8090
USER nobody
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8090"]

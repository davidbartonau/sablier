# Tiny Python HTTP service that mimics a model server's lifecycle.
# Stdlib only, no dependencies — keeps the demo dependency-free.

FROM python:3.12-alpine
WORKDIR /app
COPY main.py /app/main.py
EXPOSE 80
CMD ["python", "main.py"]

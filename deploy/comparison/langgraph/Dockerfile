FROM python:3.12-slim AS builder

WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Generate proto stubs
COPY service.proto .
RUN python -m grpc_tools.protoc \
    --proto_path=. \
    --python_out=. \
    --grpc_python_out=. \
    service.proto

# Copy application
COPY mock_llm.py agent.py ./

FROM python:3.12-slim

WORKDIR /app

# Copy installed packages and generated code
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin
COPY --from=builder /app /app

EXPOSE 50051

ENTRYPOINT ["python", "agent.py"]
CMD ["--port=50051", "--llm-latency-ms=1"]

# Dockerfile for the wrapper-manager admin CLI ("wmcli").
#
# Build context MUST be the repository root so we can pull both the proto
# file (from wrapper-shim/proto) and the Python source (from
# tools/wrapper-manager-cli/) without duplicating the proto.
#
#   docker build -t wmcli:local -f tools/wrapper-manager-cli/Dockerfile .

ARG PYTHON_VERSION=3.12

# ---------------------------------------------------------------------------
# Stage 1: generate the gRPC Python stubs from the canonical proto file.
# ---------------------------------------------------------------------------
FROM python:${PYTHON_VERSION}-slim AS protogen
WORKDIR /build

# grpcio-tools bundles its own protoc + the well-known google/protobuf protos
# (e.g. empty.proto) needed by manager.proto, so we don't need a system protoc.
RUN pip install --no-cache-dir grpcio-tools==1.73.0

COPY wrapper-shim/proto/manager.proto ./manager.proto

RUN mkdir -p /out && \
    python -m grpc_tools.protoc \
        --proto_path=. \
        --python_out=/out \
        --grpc_python_out=/out \
        --pyi_out=/out \
        manager.proto

# ---------------------------------------------------------------------------
# Stage 2: runtime image. Keep it small; just grpcio + the generated stubs.
# ---------------------------------------------------------------------------
FROM python:${PYTHON_VERSION}-slim
WORKDIR /app

RUN pip install --no-cache-dir \
        "grpcio==1.73.0" \
        "protobuf>=5.29,<7"

# Generated stubs land alongside wmcli.py so the simple `import manager_pb2`
# at the top of wmcli.py works without any sys.path gymnastics.
COPY --from=protogen /out/manager_pb2.py /app/manager_pb2.py
COPY --from=protogen /out/manager_pb2_grpc.py /app/manager_pb2_grpc.py
COPY tools/wrapper-manager-cli/wmcli.py /app/wmcli.py

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

ENTRYPOINT ["python", "/app/wmcli.py"]
CMD ["--help"]

FROM golang:1.25-bookworm AS builder

ARG RUNNER_IMAGE=ghcr.io/alex-cheehoo/gha-runner:latest
ENV RUNNER_IMAGE=${RUNNER_IMAGE}

WORKDIR /build
COPY . /tmp/scaleset

RUN cp -r /tmp/scaleset/examples/dockerscaleset/* . && \
    sed -i "s|ghcr.io/actions/actions-runner:latest|${RUNNER_IMAGE}|g" *.go && \
    go mod init build && \
    go mod edit -require github.com/actions/scaleset@v0.4.0 && \
    go mod edit -replace github.com/actions/scaleset@v0.4.0=/tmp/scaleset && \
    go mod tidy && \
    go build -o /go/bin/dockerscaleset .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates docker.io && rm -rf /var/lib/apt/lists/*
COPY --from=builder /go/bin/dockerscaleset /usr/local/bin/dockerscaleset

COPY docker/controller/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

FROM golang:1.21 as builder

WORKDIR /go/src/app

# Download modules first to allow for efficient cache usage.
COPY go.mod go.sum .
RUN go mod download

COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 go build -o /tmp/invoker ./cmd/invoker/...
RUN CGO_ENABLED=0 go build -o /tmp/createperf ./cmd/createperf/...
RUN CGO_ENABLED=0 go build -o /tmp/lifecycleperf ./cmd/lifecycleperf/...

FROM debian:bullseye

ENV DOCKER_VERSION=20.10.12 \
  GVISOR_VERSION=20230417

# Install dependencies
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    # To download docker and gVisor below.
    ca-certificates wget curl \
    # For the init script.
    jq \
  && rm -rf /var/lib/apt/lists/*

# Install docker client
RUN curl -sSL -o docker-${DOCKER_VERSION}.tgz https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz \
  && tar --strip-components 1 -xvzf docker-${DOCKER_VERSION}.tgz -C /usr/bin docker/docker \
  && rm -f docker-${DOCKER_VERSION}.tgz \
  && chmod +x /usr/bin/docker \
  && wget https://storage.googleapis.com/gvisor/releases/release/$GVISOR_VERSION/x86_64/runsc https://storage.googleapis.com/gvisor/releases/release/$GVISOR_VERSION/x86_64/containerd-shim-runsc-v1 \
  && chmod a+rx runsc containerd-shim-runsc-v1 \
  && mv runsc containerd-shim-runsc-v1 /usr/bin

COPY scripts /
COPY --from=builder /tmp/invoker /invoker
COPY --from=builder /tmp/createperf /createperf
COPY --from=builder /tmp/lifecycleperf /lifecycleperf
CMD ["/init.sh"]

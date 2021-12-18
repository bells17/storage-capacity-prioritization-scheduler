# Build the manager binary
FROM golang:1.17 as builder
WORKDIR /work

COPY ./ .
RUN CGO_ENABLED=0 go build -ldflags="-w -s" \
  -o storage-capacity-prioritization-scheduler \
  ./cmd/storage-capacity-prioritization-scheduler

# the scheduler image
FROM gcr.io/distroless/static:latest-amd64
LABEL org.opencontainers.image.source https://github.com/bells17/storage-capacity-prioritization-scheduler

COPY --from=builder /work/storage-capacity-prioritization-scheduler /storage-capacity-prioritization-scheduler
CMD ["/storage-capacity-prioritization-scheduler"]

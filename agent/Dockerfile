FROM golang:1.22-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod .
COPY pkg ./pkg
COPY cmd ./cmd

RUN mkdir -p /out && \
    for dir in ./cmd/praetor-agent-*; do \
      [ -d "$dir" ] || continue; \
      bin="$(basename "$dir")"; \
      go build -o "/out/${bin}" "$dir"; \
    done

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/praetor-agent-* /usr/local/bin/
CMD ["/bin/sh"]

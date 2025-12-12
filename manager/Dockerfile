FROM golang:1.22-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod .
COPY main.go .
COPY pkg/ ./pkg/
RUN go build -o /out/manager main.go

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/manager ./manager
CMD ["./manager"]

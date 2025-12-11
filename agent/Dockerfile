FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod ./
COPY main.go ./
RUN go build -o /out/agent main.go

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/agent ./agent
CMD ["./agent"]

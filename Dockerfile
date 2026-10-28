# Multi-stage build for Infrastructure-MCP-Server
FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/infrastructure-mcp-server ./cmd/infrastructure-mcp-server

FROM alpine:3.20

WORKDIR /app
COPY --from=builder /bin/infrastructure-mcp-server /usr/local/bin/infrastructure-mcp-server
COPY config.example.json /app/config.json

EXPOSE 8080

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/infrastructure-mcp-server"]
CMD ["-config", "/app/config.json", "-listen", ":8080"]

# syntax=docker/dockerfile:1.7

FROM node:22.23.2-alpine AS web-builder
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/package.json
RUN npm ci
COPY apps/web apps/web
RUN npm --workspace apps/web run build

FROM golang:1.27.1-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY cmd cmd
COPY internal internal
COPY migrations migrations
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentbox-server ./cmd/agentbox-server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /opt/agentbox
COPY --from=go-builder /out/agentbox-server /usr/local/bin/agentbox-server
COPY --from=web-builder /src/apps/web/dist /opt/agentbox/web
USER nonroot:nonroot
EXPOSE 8080 9443
ENTRYPOINT ["/usr/local/bin/agentbox-server"]
CMD ["--config", "/etc/agentbox/server.json"]

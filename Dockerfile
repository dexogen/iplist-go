FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM python:3.12-alpine AS icon-build

WORKDIR /src
COPY tools/generate-local-icons.py ./tools/generate-local-icons.py
ARG SNAPSHOT_URL=https://github.com/dexogen/iplist-go-sidecar/releases/download/data/manifest.json
COPY tools/download-bootstrap.py ./tools/download-bootstrap.py
RUN python3 tools/download-bootstrap.py --url "$SNAPSHOT_URL" --output /src/bootstrap
RUN python3 tools/generate-local-icons.py --repo-root /src

FROM golang:1.27-alpine AS api-build

WORKDIR /src
COPY go.mod ./
COPY internal/ ./internal/
COPY cmd/ ./cmd/
COPY --from=web-build /src/web/dist/ ./internal/app/web/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/iplistd ./cmd/iplistd

FROM alpine:3.22

RUN apk add --no-cache ca-certificates && adduser -D -H iplist
WORKDIR /app
RUN mkdir -p /app/runtime/dns && chown -R iplist:iplist /app/runtime
COPY --from=api-build /out/iplistd /usr/local/bin/iplistd
COPY --from=icon-build /src/bootstrap ./bootstrap/
COPY --from=icon-build /src/storage ./storage/
USER iplist

EXPOSE 8080

CMD ["iplistd"]

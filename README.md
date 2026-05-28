<p align="center">
  <img src=".github/assets/header.png" alt="iplist-go" width="100%">
</p>

[![Tests](https://github.com/dexogen/iplist-go/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/dexogen/iplist-go/actions/workflows/test.yml)
[![Build image](https://github.com/dexogen/iplist-go/actions/workflows/build-image.yml/badge.svg?branch=main)](https://github.com/dexogen/iplist-go/actions/workflows/build-image.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io%2Fdexogen%2Fiplist--go-latest-blue)](https://github.com/dexogen/iplist-go/pkgs/container/iplist-go)


# iplist-go

Selfhosted-приложение, предоставляющее API со списками IP-адресов, сетей и доменов для различных сервисов. Вы сами сможете найти подходящее применение данным спискам. Например, чтобы скормить их UniFi, MikroTik, ipset/nftables или просто заблокировать себе что-нибудь вредное.

Идейный форк [`rekryt/iplist`](https://github.com/rekryt/iplist), переписанный на Go + Svelte UI. Опирается на конфигурационные файлы исходного проекта, но использует иные механизмы оптимизации списков. На создание подтолкнуло желание добавить поддержку UniFi-формата, которого не было в оригинальном проекте, но в итоге привело к полной переработке.

Конфиги живут в отдельном репозитории [`dexogen/iplist-go-sidecar`](https://github.com/dexogen/iplist-go-sidecar), который используется при сборке образа.

## Запуск

```bash
cp .env.example .env
docker compose up -d --build
```

http://127.0.0.1:8080

## API

```bash
curl "http://127.0.0.1:8080/api/latest/export?format=unifi&data=ipv4"
```

Наборы данных: `/`, `/beta`, `/russia`.
API-префиксы: `/api/latest/`, `/api/beta/`, `/api/russia/`.

## Поддерживаемые форматы

- `json`
- `text`
- `unifi`
- `mikrotik`
- `ipset`
- `nfset`
- `amnezia`

Данные: `domains`, `ip4`, `ip6`, `cidr4`, `cidr6`, `ipv4`, `ipv6`.

## DNS-refresh

Адреса, в которые резолвятся те или иные домены, могут отличаться от региона к региону и меняться со временем. Поэтому при активации этой функции iplist-go будет периодически резолвить домены и сохранять найденные IP в runtime-файлах. Это позволяет поддерживать списки в актуальном конкретно для вашей локации состоянии.

```dotenv
IPLIST_DNS_REFRESH_ENABLED=true
IPLIST_DNS_REFRESH_CRON="0 */12 * * *"
IPLIST_DNS_REFRESH_TIMEZONE="UTC"
IPLIST_DNS_REFRESH_TIMEOUT="4s"
IPLIST_DNS_REFRESH_CONCURRENCY=8
IPLIST_DNS_RUNTIME_DIR="runtime/dns"
```

Runtime-файлы:

- `runtime/dns/<config-set>/<group>/<site>.json` - локально найденные IP;
- `runtime/dns/.status/<config-set>.json` - статус последнего прогона;
- `runtime/export-cache/<config-set>/...` - готовые full exports.

В `docker-compose.yml` runtime хранится в named volume `dns-runtime`.

## Разработка

```bash
go test ./...
cd web
npm ci
npm run build
```

Ручная сборка образа:

```bash
docker build -t iplist-go:local .
```

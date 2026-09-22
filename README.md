<p align="center">
  <img src=".github/assets/header.png" alt="iplist-go" width="100%">
</p>

[![Tests](https://github.com/dexogen/iplist-go/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/dexogen/iplist-go/actions/workflows/test.yml)
[![Build image](https://github.com/dexogen/iplist-go/actions/workflows/build-image.yml/badge.svg?branch=main)](https://github.com/dexogen/iplist-go/actions/workflows/build-image.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io%2Fdexogen%2Fiplist--go-latest-blue)](https://github.com/dexogen/iplist-go/pkgs/container/iplist-go)


# iplist-go

Selfhosted-приложение, предоставляющее API со списками IP-адресов, сетей и доменов для различных сервисов. Вы сами сможете найти подходящее применение данным спискам. Например, чтобы скормить их UniFi, MikroTik, ipset/nftables или просто заблокировать себе что-нибудь вредное.

Идейный форк [`rekryt/iplist`](https://github.com/rekryt/iplist), переписанный на Go + Svelte UI. Опирается на конфигурационные файлы исходного проекта, но использует иные механизмы оптимизации списков. На создание подтолкнуло желание добавить поддержку UniFi-формата, которого не было в оригинальном проекте, но в итоге привело к полной переработке.

Списки собираются в [`dexogen/iplist-go-sidecar`](https://github.com/dexogen/iplist-go-sidecar) и публикуются с контрольными суммами в GitHub Release. Работающее приложение проверяет обновления при старте и каждые 30 минут. Обновление списков не требует пересборки образа или перезапуска контейнера.

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

## Обновление списков

```dotenv
IPLIST_SNAPSHOT_URL="https://github.com/dexogen/iplist-go-sidecar/releases/download/data/manifest.json"
IPLIST_SNAPSHOT_INTERVAL="30m"
IPLIST_SNAPSHOT_TIMEOUT="3m"
IPLIST_SNAPSHOT_MAX_AGE="48h"
IPLIST_SNAPSHOT_DIR="runtime/snapshots"
```

Скачиваются только изменившиеся сжатые объекты. SHA-256, размеры, структура JSON, IP/CIDR и счетчики проверяются до активации. Каталог и готовые экспорты переключаются вместе; ошибка оставляет прежнюю версию рабочей. В volume хранятся текущий и предыдущий проверенные снимки. Образ содержит резервный снимок для первого запуска без сети, а при повторном запуске используется persistent volume.

`GET /healthz` проверяет процесс; `GET /readyz` возвращает 200 при наличии проверенного снимка, включая сохраненный снимок без сети. Свежесть каждого источника отражается отдельно в `/api/<set>/runtime` и заголовках экспортов:

- `X-IPList-Snapshot` — контрольная сумма объекта набора;
- `X-IPList-Source-Status` — `ok`, `degraded` или `stale`;
- `X-IPList-Source-Updated-At` — последнее полное успешное получение источника;
- `X-IPList-Export-Generated-At` — время подготовки кэшированного экспорта.

Время проверки манифеста не подменяет время свежести источника. Поврежденный или недоступный источник сохраняется с соответствующим статусом. После ошибки сети повторная проверка выполняется примерно через минуту.

## DNS-refresh

Адреса, в которые резолвятся те или иные домены, могут отличаться от региона к региону и меняться со временем. Поэтому при активации этой функции iplist-go будет периодически резолвить домены и сохранять найденные IP в runtime-файлах. Это позволяет поддерживать списки в актуальном конкретно для вашей локации состоянии.

```dotenv
IPLIST_DNS_REFRESH_ENABLED=true
IPLIST_DNS_REFRESH_CRON="0 */12 * * *"
IPLIST_DNS_REFRESH_TIMEZONE="UTC"
IPLIST_DNS_REFRESH_TIMEOUT="4s"
IPLIST_DNS_REFRESH_CONCURRENCY=8
IPLIST_DNS_RUNTIME_DIR="runtime/dns"
IPLIST_DNS_REFRESH_ON_START="true"
IPLIST_DNS_SERVERS=""
IPLIST_DNS_USE_SOURCE_SERVERS="false"
IPLIST_DNS_STALE_AFTER="168h"
```

По умолчанию используется системный DNS контейнера. Можно указать `IPLIST_DNS_SERVERS="192.168.1.1:53,192.168.1.2:53"`. DNS из исходных конфигов применяется только при `IPLIST_DNS_USE_SOURCE_SERVERS=true`, если общий список не задан. Порядок серверов сохраняется.

A и AAAA имеют отдельные таймауты. Ответы хранятся по доменам и семействам адресов: таймаут одного запроса не стирает остальные результаты. Отрицательный ответ требует подтверждения следующим проходом; старые положительные ответы при сбоях сохраняются не дольше `IPLIST_DNS_STALE_AFTER`. Счетчики `failed` и `retained` видны в runtime API. Общие домены разрешаются один раз за проход для одной политики резолвера.

Новый снимок активируется до окончания DNS-прохода. После обогащения публикуется согласованная версия экспортов; незавершенный проход старой основы отменяется при ее замене.

Runtime-файлы:

- `runtime/dns/<config-set>/<group>/<site>.json` - локально найденные IP;
- `runtime/dns/.status/<config-set>.json` - статус последнего прогона;
- `runtime/dns/.domains/<config-set>/<site>.json` — ответы по доменам и семействам IP;
- `runtime/snapshots/` — текущий и предыдущий снимки;
- `runtime/export-cache/generation-*/` — готовые экспорты активного поколения.

В `docker-compose.yml` runtime хранится в named volume `dns-runtime`.

## Разработка

```bash
go test -race ./...
cd web
npm ci
npm run build
```

Ручная сборка образа:

```bash
docker build -t iplist-go:local .
```

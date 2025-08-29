| Kafka | sticky/hash/round_robin балансеры (canonical names: sticky, round_robin, hash) — legacy aliases (least_bytes, sticky) поддерживаются, acks настраиваемы |
# alloy-distributor (pulse-loki-produce metrics, ConfigMap + Hot Reload)

Лёгкая прослойка между Alloy / Loki Exporter и децентрализованными Loki кластерами.
Принимает push (protobuf/json), не парсит payload, синхронно отправляет в Kafka (ACK=лидер), предоставляет наблюдаемость и горячую перезагрузку конфигурации из ConfigMap.

---

## Основные возможности

| Категория | Возможности |
|-----------|-------------|
| Endpoints | POST /loki/api/v1/push, /api/prom/push, GET /ready, /metrics, /configz, POST /reload |
| Конфиг | YAML (ConfigMap) + горячая перезагрузка (/reload или SIGHUP) |
| Kafka | sticky/hash/round_robin балансеры, acks настраиваемы |
| Надёжность | ACK=1 (опционально -1), health gate по error rate + consecutive errors |
| Rate limiting | Глобальный и per-tenant token bucket (горячо обновляемые) |
| Метрики | Префикс pulse_loki_produce_, гистограммы latency Kafka/HTTP, классификация ошибок |
| Canary | Утилита генерации нагрузки (cmd/pulse-loki-canary) |
| Логи | JSON structured (уровень, tenant, размер, время Kafka) |
| Build info | Метрика pulse_loki_produce_build_info + hash конфигурации |
| Reload | Иммутабельные поля → пересоздание Kafka writer; мутируемые → обновление в памяти |

---

## Конфигурация (config.yaml)

Пример: см. `config/config.yaml`.

| Поле | Назначение | Reload |
|------|------------|--------|
| kafka_brokers | Список брокеров | Иммутабельно (writer rebuild) |
| kafka_topic | Kafka topic | Иммутабельно |
| kafka_required_acks | -1/1/0 | Иммутабельно |
| kafka_balancer | sticky/hash/round_robin | Иммутабельно |
| kafka_write_timeout | Таймаут записи | Иммутабельно (для простоты) |
| kafka_batch_timeout | Интервал флеша батча | Иммутабельно |
| kafka_batch_size | Максимум сообщений в батче | Иммутабельно |
| kafka_batch_bytes | Максимум байт в батче | Иммутабельно |
| max_body_bytes | Лимит входящего тела | Динамически |
| allow_empty_tenant | Разрешить пустой tenant | Динамически |
| default_tenant | Tenant по умолчанию | Динамически |
| metrics_enable_tenant_label | Включить label tenant | Требует рестарт (метрики) |
| health_* | Порог/интервал health | Динамически |
| sla_gauge_enable | Включить SLA gauge | Динамически |
| rate_limit_* | Лимиты RPS | Динамически (лиматоры пересоздаются) |
| log_level | info|debug | Динамически (в текущей версии используется только при старте логики условных сообщений) |
| quiet | Подавить info логи | Динамически |
| port | Listen порт | Иммутабельно (перезапускайте Pod) |

---

## Горячая перезагрузка

1. Обновить ConfigMap:
```
kubectl apply -f deploy/configmap.yaml
```
2. Вызвать:
```
curl -X POST http://dist:3101/reload
```
или послать сигнал внутри Pod:
```
kubectl exec <pod> -- kill -HUP 1
```
3. Проверить логи: `immutable config changed` если пересоздан Kafka writer.

---

## Метрики (основные)

| Имя | Тип | Лейблы | Описание |
|-----|-----|--------|----------|
| pulse_loki_produce_requests_total | counter | endpoint,result,content_type_class[,tenant] | Результаты обработки |
| pulse_loki_produce_request_bytes_total | counter | endpoint[,tenant] | Байты тел |
| pulse_loki_produce_kafka_write_duration_seconds | histogram | result | Латентность записи Kafka |
| pulse_loki_produce_kafka_write_errors_total | counter | error_type | Классифицированные ошибки |
| pulse_loki_produce_kafka_consecutive_error_count | gauge | — | Число подряд ошибок |
| pulse_loki_produce_rate_limited_total | counter | scope=global|tenant | Ограниченные запросы |
| pulse_loki_produce_request_duration_seconds | histogram | endpoint,result | End-to-end HTTP |
| pulse_loki_produce_health_up | gauge | — | 1 здоров, 0 деградация |
| pulse_loki_produce_sla_success_ratio | gauge | — | SLA интервала |
| pulse_loki_produce_build_info | gauge | version,commit,date,go_version | Build info |

Kafka error_type: timeout, not_leader, unknown_topic, too_large, conn_refused, conn_reset, network, other.

---

## Canary – что тестируем

| Сценарий | Как запускать | Метрики |
|----------|---------------|---------|
| Базовый RPS | `-rps 200` стабильный поток | requests_total rate, latency histogram |
| Burst | Увеличить rps x2-x5 | p95 latency, rate_limited_total |
| Отказ одной Kafka ноды | Остановить брокер | kafka_write_errors_total{error_type="not_leader"/"timeout"} всплеск |
| Полный outage Kafka | Остановить все брокеры | error rate > threshold, health_up=0 |
| Пропадание входного трафика | Остановить canary | NoTraffic алерт |
| Rate limiting | Настроить лимиты ниже канарей | rate_limited_total рост, latency стабильна |
| Hash порядок | balancer=hash + несколько tenants | Проверить распределение по партициям |

p95 latency:
```
histogram_quantile(0.95, sum(rate(pulse_loki_produce_kafka_write_duration_seconds_bucket[5m])) by (le))
```

Error rate:
```
sum(rate(pulse_loki_produce_requests_total{result="kafka_error"}[5m])) / sum(rate(pulse_loki_produce_requests_total[5m]))
```

---

## Пример запуска

```
go build -ldflags "-X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Version=1.1.0 -X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Commit=$(git rev-parse --short HEAD) -X github.com/DeveloperDarkhan/loki-producer/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o alloy-distributor ./cmd/alloy-distributor
./alloy-distributor -config.file=./config/config.yaml
```

Reload:
```
curl -X POST http://localhost:3101/reload
```

---

## Deployment без ConfigMap?

- Раньше ENV -> изменение требовало redeploy.
- Сейчас ConfigMap + /reload → позволяет менять лимиты, пороги без рестарта.
- Immutable поля (Kafka brokers/topic/balancer/acks) всё равно требуют перезапуска (writer rebuild выполнится автоматом при reload — но порт менять нельзя без перезапуска Pod).
- Итог: ConfigMap + /reload уменьшает время конфигурационных изменений и риск ошибки при массовых рестартах.

---

## PROD CHECK-LIST

| # | Пункт | Статус |
|---|-------|--------|
| ✅ 1 | ConfigMap + hot reload внедрены |  |
| ✅ 2 | Классификация ошибок Kafka |  |
| ✅ 3 | Build info метрика + hash конфига |  |
| ✅ 4 | Canary доступен |  |
| ✅ 5 | Health + SLA метрики |  |
| ✅ 6 | Rate limiting (глобальный/tenant) |  |
| ☐ 7 | Kafka replication factor & min.insync.replicas проверены |  |
| ☐ 8 | TLS/SASL к Kafka при необходимости |  |
| ☐ 9 | Алерты задеплоены (latency, errors, no traffic, consecutive) |  |
| ☐ 10 | Dashboard (latency p95/p99, error_type, rate) |  |
| ☐ 11 | Нагрузочный тест с пиковым RPS проведён |  |
| ☐ 12 | Документ rollback (переключить Alloy напрямую в Loki) |  |
| ☐ 13 | DR план при полном Kafka outage |  |
| ☐ 14 | Решение о retry (0 или 1) документировано |  |
| ☐ 15 | tenant label: оставлено false или оценен риск |  |
| ☐ 16 | Лимиты RPS калиброваны (нет ложных 429) |  |
| ☐ 17 | Секреты (если появятся) отдельно (Secret) |  |
| ☐ 18 | Обновление ConfigMap → /reload проверено |  |

---

## Ограничения

- Нет retry слоя (умышленно для низкой латентности) — можно добавить 1 retry для timeout/not_leader.
- Изменение метрики tenant label требует рестарт (переинициализация registry).
- Нет persistent buffering (Kafka — единственный буфер).
- Нет TLS/SASL примера (зависит от вашей инфраструктуры).

---

## Возможные улучшения

| Улучшение | Эффект |
|-----------|--------|
| Retry (1 attempt selective) | Снижение временных отказов |
| Circuit breaker | Быстрый отказ при перманентной деградации |
| Batch size histogram | Анализ распределения push размеров |
| pprof / tracing | Глубокая диагностика производительности |
| /healthz (детализ.) | Отдельный статус без gauge интерпретации |

---

## Лицензия

(Заполните согласно корпоративной политике.)

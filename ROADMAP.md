# alloy-distributor (pulse-loki-produce) — Руководство по Погружению в Код

Это практический роадмап для поэтапного изучения проекта `alloy-distributor`.  
Следуй шаг за шагом: от общей карты кода до глубокой диагностики и улучшений.  
Документ можно использовать как чеклист, учебник и основу для внутренних материалов (INTERNAL.md / ONCALL.md).

---

## Оглавление
1. [Обзор проекта](#1-обзор-проекта)
2. [Карта каталогов](#2-карта-каталогов)
3. [Быстрый старт](#3-быстрый-старт)
4. [Общий поток данных](#4-общий-поток-данных)
5. [Этапы Разбора (20 фаз)](#5-этапы-разбора-20-фаз)
6. [PromQL Шпаргалка](#6-promql-шпаргалка)
7. [Canary: сценарии тестирования](#7-canary-сценарии-тестирования)
8. [Метрики и их семантика](#8-метрики-и-их-семантика)
9. [Kafka Ошибки и Классификация](#9-kafka-ошибки-и-классификация)
10. [Конфигурация и Hot Reload](#10-конфигурация-и-hot-reload)
11. [Health / SLA / Error Rate](#11-health--sla--error-rate)
12. [Rate Limiting](#12-rate-limiting)
13. [Логирование (JSON)](#13-логирование-json)
14. [Checklist для Production](#14-checklist-для-production)
15. [Бэклог Улучшений](#15-бэклог-улучшений)
16. [Диагностика Инцидентов (OnCall Mini Guide)](#16-диагностика-инцидентов-oncall-mini-guide)
17. [Архитектурная Краткая Справка (1‑страничник)](#17-архитектурная-краткая-справка-1-страничник)
18. [Дорожная Карта Обучения по Дням](#18-дорожная-карта-обучения-по-дням)
19. [Контрольные Вопросы (Самопроверка)](#19-контрольные-вопросы-самопроверка)
20. [Шаблоны внутренних документов](#20-шаблоны-внутренних-документов)
21. [Приложение: Быстрые команды](#21-приложение-быстрые-команды)

---

## 1. Обзор проекта

`alloy-distributor` — прослойка (ingest gateway) между Alloy / Loki exporters и Kafka.  
Цель: принимать push логов (Loki protobuf или JSON), не парсить, быстро ACK'нуть, положить в Kafka, где дальнейшие consumers разнесут данные по децентрализованным Loki кластерам.

Ключевые принципы:
- Минимум логической обработки — максимум стабильности.
- Синхронная запись в Kafka (acks=1) → приемлемая надёжность и низкая latency.
- Наблюдаемость: метрики (гистограммы), классификация типов ошибок Kafka, SLA, health.
- Горячий reload конфигурации (ConfigMap + /reload + SIGHUP).
- Управление нагрузкой (Rate limiting, global & per-tenant).
- Canary для генерации синтетической нагрузки и наблюдения деградаций.

---

## 2. Карта каталогов

```
cmd/
  alloy-distributor/         # основной бинарь
  pulse-loki-canary/         # нагрузочный генератор
internal/
  buildinfo/                 # метрика версии + лог старта
  config/                    # парсер YAML конфигурации + reload различение immutable/mutable
  kafka/                     # тонкая обёртка над kafka-go writer
  metrics/                   # регистрация и структура Prometheus метрик
  server/                    # HTTP сервер, обработка push, health, reload, rate limits
deploy/                      # манифесты K8s (Deployment, ConfigMap, PDB)
alerts/                      # правила Prometheus (recording / alert rules)
config/                      # пример config.yaml
README.md                    # пользовательская документация
CODE-ROADMAP.md              # этот документ
```

---

## 3. Быстрый старт

1. Запусти локально Kafka (или Redpanda).
2. Скопируй `config/config.yaml`, поправь `kafka_brokers`.
3. Собери:
   ```
   go build -ldflags "-X github.com/your-org/alloy-distributor/internal/buildinfo.Version=dev-local" -o alloy-distributor ./cmd/alloy-distributor
   ./alloy-distributor -config.file=./config/config.yaml
   ```
4. Проверка:
   - `curl -v -H "X-Scope-OrgID: t1" -d '{}' http://localhost:3101/loki/api/v1/push`
   - `curl http://localhost:3101/metrics | grep pulse_loki_produce`
   - `curl http://localhost:3101/configz`
5. Запусти canary:
   ```
   go run ./cmd/pulse-loki-canary -rps 100 -concurrency 4 -tenant canary
   ```

---

## 4. Общий поток данных

```
Alloy / Exporter
   -> HTTP POST /loki/api/v1/push
       -> (server.handlePush)
           -> Проверка tenant
           -> Rate limiting (global / tenant)
           -> MaxBytesReader + чтение тела
           -> Формирование kafka.Message
           -> kafka.Writer.Write (acks=1)
           -> Метрики, логи
           -> 204 No Content
   -> Kafka topic (буфер)
       -> (будущие consumers) -> децентрализованные Loki кластеры
```

---

## 5. Этапы Разбора (20 фаз)

| Фаза | Цель | Файлы | Ключевые вопросы |
|------|------|-------|------------------|
| 0 | Подготовка среды | main.go | Как запускаем? |
| 1 | Архитектура | README, дерево | Какие пакеты? |
| 2 | Жизненный цикл | main.go, server.go | Как завершается? |
| 3 | Конфиг + reload | config/config.go | Что immutable? |
| 4 | HTTP pipeline | server/server.go | Где меняется result? |
| 5 | Kafka writer | kafka/writer.go | Почему async=false? |
| 6 | Метрики | metrics/metrics.go | Что даёт histogram? |
| 7 | Health/SLA | server.healthLoop() | Что опускает health? |
| 8 | Rate limiting | ratelimit.go | Как рассчитывается burst? |
| 9 | Логи (JSON) | server.jsonLog() | Какие ключи логов? |
| 10 | Reload потоки | server.Reload() | Что пересоздаётся? |
| 11 | Canary | cmd/pulse-loki-canary/main.go | Для чего jitter? |
| 12 | Kafka ошибки | classifyKafkaError | Какие классы? |
| 13 | Deployment | deploy/*.yaml | Как подключён ConfigMap? |
| 14 | Alerts | alerts/*.yaml | Порог error rate? |
| 15 | Risk review | README, код | Где возможны потери? |
| 16 | Нагрузочные сценарии | canary + metrics | Где узкое место? |
| 17 | Диагностика | metrics + logs | Что проверять первым? |
| 18 | Улучшения | Backlog | Чем заняться потом? |
| 19 | Документирование | этот файл | Что включить в INTERNAL.md? |
| 20 | Самопроверка | см. секцию 19 | Готов ли к поддержке? |

Каждая фаза описана подробнее ниже.

### Фаза 0–4: Основа
- Прочти `cmd/alloy-distributor/main.go`: загрузка конфига → создание сервера → сигналы → reload.
- `server.New`: сборка mux, маршруты `/loki/api/v1/push`, `/api/prom/push`, `/reload`, `/configz`, `/metrics`, `/ready`.
- `wrapRequest`: измерение времени и финального статуса.
- `handlePush`: порядок действий, включай комментарии в IDE.

### Фаза 5–6: Kafka & Метрики
- Изучи writer: как выбирается balancer.
- Проверь какие ошибки попадают в какую метрику.
- Посмотри назначение bucket’ов гистограмм.

### Фаза 7–8: Health & Rate Limit
- Разбери healthLoop: вычисление дельт counters.
- Поиграйся с `health_error_rate_threshold` (понизь — увидь health_up=0).
- Rate limiting: глобальный лимит + per-tenant (две независимые проверки).

### Фаза 9–10: Логи & Reload
- JSON формируется в `jsonLog`.
- После reload immutable поля → rebuild writer (закрывает старый, создаёт новый).

### Фаза 11–12: Canary & Ошибки
- Canary параметризация: rps, concurrency, streams/lines.
- Ошибки Kafka — сопоставь текст ошибочных случаев (симуляцией) с метками error_type.

### Фаза 13–15: Deployment / Alerts / Риски
- Deployment: ConfigMap монтируется в `/config/config.yaml`.
- Alerts: пороги (latency, error rate, no traffic).
- Риски: нет retry, tenant label выключен, нет TLS/SASL (если надо — внедрить).

### Фаза 16–18: Нагрузка / Диагностика / Улучшения
- Используй canary для burst’ов.
- Снять метрики p95/p99 Kafka latency.
- Сформировать backlog (ниже).

### Фаза 19–20: Самопроверка и Документирование
- Ответь на контрольные вопросы.
- Подготовь INTERNAL.md и ONCALL.md основываясь на этом документе.

---

## 6. PromQL Шпаргалка

| Цель | Запрос |
|------|--------|
| RPS (общий) | `sum(rate(pulse_loki_produce_requests_total[1m]))` |
| Error rate (5m) | `sum(rate(pulse_loki_produce_requests_total{result="kafka_error"}[5m])) / sum(rate(pulse_loki_produce_requests_total[5m]))` |
| p95 Kafka latency | `histogram_quantile(0.95, sum(rate(pulse_loki_produce_kafka_write_duration_seconds_bucket[5m])) by (le))` |
| p99 Kafka latency | `histogram_quantile(0.99, sum(rate(pulse_loki_produce_kafka_write_duration_seconds_bucket[5m])) by (le))` |
| Консекутивные ошибки | `pulse_loki_produce_kafka_consecutive_error_count` |
| SLA (15m) | `sum(rate(pulse_loki_produce_requests_total{result="success"}[15m])) / sum(rate(pulse_loki_produce_requests_total[15m]))` |
| No traffic (1m == 0) | `sum(rate(pulse_loki_produce_requests_total[1m])) == 0` |
| Rate limited доля | `sum(rate(pulse_loki_produce_rate_limited_total[5m])) / sum(rate(pulse_loki_produce_requests_total[5m]))` |
| Kafka error type breakdown | `sum(rate(pulse_loki_produce_kafka_write_errors_total[5m])) by (error_type)` |

---

## 7. Canary: сценарии тестирования

| Сценарий | Действие | Ожидание |
|----------|----------|----------|
| Базовый поток | `-rps 100` | Стабильные низкие latency |
| Burst | Увеличить `-rps` до 500 | p95 растёт умеренно, ошибок мало |
| Rate limit | Установить лимит ниже RPS | Рост `rate_limited_total` без всплеска `kafka_error` |
| Отказ 1 брокера | Остановить 1 брокер | Ошибки `not_leader` / короткие `timeout` |
| Полный outage | Остановить всех | error rate → 1, health_up=0 |
| Пропадание трафика | Остановить canary | NoTraffic алерт спустя 2m |
| hash баланс | `kafka_balancer=hash`, 2 tenants | Порядок внутри tenant (устойчивый партиционированный ключ) |

---

## 8. Метрики и их семантика

| Метрика | Значение |
|---------|----------|
| pulse_loki_produce_requests_total | Итог классификации обработки запроса |
| pulse_loki_produce_request_bytes_total | Объём входящих данных |
| pulse_loki_produce_kafka_write_duration_seconds | Чистое время записи в Kafka |
| pulse_loki_produce_kafka_write_errors_total | Счётчик ошибок по типу |
| pulse_loki_produce_kafka_consecutive_error_count | Макс подряд ошибок текущей серии |
| pulse_loki_produce_rate_limited_total | Отказы из-за лимитов |
| pulse_loki_produce_request_duration_seconds | End-to-end HTTP время |
| pulse_loki_produce_health_up | 1/0 индикатор здоровья |
| pulse_loki_produce_sla_success_ratio | SLA окна |
| pulse_loki_produce_build_info | Версия + commit + date |

---

## 9. Kafka Ошибки и Классификация

| error_type | Типовая причина | Пример текста |
|------------|-----------------|---------------|
| timeout | Таймаут ACK | "context deadline exceeded" |
| not_leader | Смена лидера / ISR | "not leader for partition" |
| unknown_topic | Topic не существует | "unknown topic or partition" |
| too_large | Размер сообщения > max.message.bytes | "message too large" |
| conn_refused | Брокер недоступен | "connection refused" |
| conn_reset | Разрыв соединения | "connection reset by peer" |
| network | Другая сет. ошибка | generic net.Error |
| other | Иное | всё остальное |

---

## 10. Конфигурация и Hot Reload

- Источник: `config/config.yaml` (ConfigMap).
- POST `/reload` или SIGHUP → попытка загрузки + валидация.
- Immutable поля → rebuild Kafka writer (закрытие старого, создание нового).
- Tenant label toggle требует рестарт (иначе Prometheus registry конфликт).

Проверка:
```
kubectl apply -f configmap.yaml
curl -X POST http://dist:3101/reload
curl http://dist:3101/configz
```

---

## 11. Health / SLA / Error Rate

Алгоритм (каждый `health_eval_period`):
1. Снимает дельты total / success / error.
2. `error_rate = dErr / dTotal`.
3. Если `error_rate > threshold` ИЛИ `consecutive_errors >= threshold` → health_up=0.
4. SLA gauge = `dSuccess / dTotal`.

Health сигнализирует деградацию, но не останавливает приём — решение об отказе можно добавить позже (circuit breaker).

---

## 12. Rate Limiting

Два уровня:
- Глобальный (один токен-бакет).
- Per-tenant (map[tenant]*Limiter).

Поля:
- `rate_limit_global_rps`, `rate_limit_global_burst`
- `rate_limit_per_tenant_rps`, `rate_limit_per_tenant_burst`

При отказе (нет токена) → HTTP 429, метрика `rate_limited_total{scope=...}`.

---

## 13. Логирование (JSON)

Примеры:
```
{"level":"info","msg":"accepted","tenant":"t1","bytes":1234,"kafka_ms":2.1,"endpoint":"/loki/api/v1/push","ts":"..."}
{"level":"warn","msg":"kafka write failed","tenant":"t1","bytes":1234,"kafka_ms":101.3,"error":"context deadline exceeded","error_type":"timeout","ts":"..."}
```

Ключи:
- level, msg
- tenant
- bytes
- kafka_ms
- error / error_type (при ошибках)
- endpoint
- ts

---

## 14. Checklist для Production

(Отмечай по мере выполнения)

| ✅ / ☐ | Пункт |
|--------|-------|
| ☐ | Kafka replication factor ≥3 и min.insync.replicas ≥2 |
| ☐ | TLS/SASL (при необходимости сетевой сегментации) |
| ☐ | Alert правила задеплоены (latency, error rate, no traffic, consecutive) |
| ☐ | Dashboard: RPS, p95/p99 latency, error_type breakdown |
| ☐ | Нагрузочное тестирование (пиковый RPS, burst) |
| ☐ | DR план при полном Kafka outage |
| ☐ | Документ rollback (переключить Alloy напрямую на Loki) |
| ☐ | Решение по retries (0 или 1 выборочно по timeout/not_leader) |
| ☐ | Calibrate rate limits (нет ложных 429) |
| ☐ | tenant label оставлен выключенным (подтверждено) |
| ☐ | Отработана /reload процедура в staging |
| ☐ | Canary постоянно или по крону запущен |
| ☐ | Мониторинг NoTraffic алерт протестирован |
| ☐ | BuildInfo версионирование внедрено в CI |
| ☐ | Логирование агрегируется в центральный лог-пайплайн |
| ☐ | Риск анализа потерь при acks=1 принят (или переход на -1) |

---

## 15. Бэклог Улучшений

| Приоритет | Улучшение | Польза |
|-----------|-----------|--------|
| Высокий | Добавить selective retry (timeout/not_leader) | Меньше transient ошибок |
| Средний | Circuit breaker при health_up=0 | Быстрый отказ → разгрузка Alloy |
| Средний | Batch size histogram | Наблюдение за распределением объёмов |
| Средний | pprof / tracing | Диагностика performance |
| Средний | Dead-letter topic (DLQ) | Сохранение проблемных сообщений |
| Низкий | /healthz с деталями (JSON snapshot) | Улучшение observability |
| Низкий | Structured error taxonomy refactor | Более точная RCA |
| Низкий | Configurable per-endpoint limits | Гибкость будущих API |

---

## 16. Диагностика Инцидентов (OnCall Mini Guide)

| Симптом | Действия | Метрики |
|---------|----------|---------|
| Рост latency | Проверить p95/p99 Kafka write | `pulse_loki_produce_kafka_write_duration_seconds` |
| Рост error rate | Классифицировать error_type | `*_kafka_write_errors_total{error_type}` |
| health_up=0 | Сравнить error rate и consecutive_error_count | `pulse_loki_produce_health_up`, `*_consecutive_error_count` |
| 429 ответы | Проверить лимиты | `pulse_loki_produce_rate_limited_total` |
| No traffic | Проверить Alloy/ingress | RPS=0, алерт NoTraffic |
| Всплеск not_leader | Проверить Kafka re-election / контроллер | error_type="not_leader" |
| timeout ошибки | Проверить сеть / overloaded brokers | error_type="timeout" |
| unknown_topic | Проверить конфиг reload / orthography | error_type="unknown_topic" |

---

## 17. Архитектурная Краткая Справка (1‑страничник)

- Вход: HTTP POST (Loki push) → tenant заголовок X-Scope-OrgID.
- Обработка: лимиты → чтение тела → Kafka → 204.
- Наблюдаемость: метрики (latency, errors, SLA, health).
- Надёжность: Kafka как буфер (acks=1).
- Гибкость: ConfigMap + /reload (горячие изменения).
- Расширяемость: retry / circuit breaker / consumer side replication.

---

## 18. Дорожная Карта Обучения по Дням

| День | Задачи |
|------|--------|
| 1 | Фазы 1–4 (архитектура, жизненный цикл, HTTP pipeline) |
| 2 | Фазы 5–8 (Kafka, метрики, health, rate limits) |
| 3 | Фазы 9–12 (логи, reload, canary, ошибки) |
| 4 | Фазы 13–16 (деплой, алерты, нагрузка) |
| 5 | Фазы 17–20 (диагностика, документация, самопроверка) |

---

## 19. Контрольные Вопросы (Самопроверка)

1. Какие условия опускают `health_up` в 0?  
2. Почему нельзя безопасно горячо включить tenant label?  
3. Чем отличается `kafka_write_duration_seconds` от `request_duration_seconds`?  
4. В какой момент формируется `kafka_error` результат?  
5. Что произойдёт при изменении `kafka_topic` через /reload?  
6. Как классифицируется ошибка таймаута?  
7. Как SLA метрика вычисляется и когда обновляется?  
8. Почему histogram предпочтительнее summary для p95/p99 в прометеусе?  
9. Как работает глобальный и per-tenant rate limit вместе?  
10. Какие поля конфига считаются immutable?  
11. Какие компромиссы у acks=1 vs acks=-1?  
12. При каком сценарии может быть “тихий” рост latency без ошибок?  
13. Что будет, если тело превышает `max_body_bytes`?  
14. Что даёт hash balancer + msg.Key=tenant?  
15. Как быстро диагностировать пропадание трафика?  

---

## 20. Шаблоны внутренних документов

### INTERNAL.md (Skeleton)
```
# INTERNAL

## Request Flow
(кратко шаги)

## Config (Immutable vs Mutable)
таблица

## Kafka Interaction
acks, balancers, error types

## Metrics Map
таблица метрик и смысл

## Health Logic
формула error_rate, условия unhealthy

## Rate Limiting
алгоритм + параметры

## Reload
что rebuild'ится, что нет
```

### ONCALL.md (Skeleton)
```
# ONCALL PLAYBOOK

## Symptoms → Actions
таблица (см. раздел 16)

## First 5 Min
1. Проверить health_up
2. Посмотреть error_type распределение
3. p95 latency
4. RPS baseline
5. Rate limiting counters

## Escalate When
(критерии)

## Rollback
(описание шагов)
```

---

## 21. Приложение: Быстрые команды

| Цель | Команда |
|------|---------|
| Локальный запуск | `./alloy-distributor -config.file=./config/config.yaml` |
| Reload | `curl -X POST :3101/reload` |
| Проверить конфиг | `curl :3101/configz | jq` |
| Метрики | `curl :3101/metrics` |
| Canary старт | `go run ./cmd/pulse-loki-canary -rps 200 -tenant canary` |
| Измерить p95 | PromQL (см. шпаргалку) |
| SIGHUP | `kill -HUP <pid>` |
| Лимит тест | Понизить rate_limit_global_rps → /reload |
| Ошибка unknown_topic | Изменить topic на несуществующий + reload |

---

## Заключение

Этот роадмап предназначен для системного и безопасного погружения:  
От общего → к деталям → к эксплуатации и улучшениям.  
Отмечай прогресс, фиксируй вопросы, адаптируй под команду.  

Если потребуются дополнительные разделы (например, глубже про retries или circuit breaker дизайн) — добавь их как расширения в конце.

Удачной работы!

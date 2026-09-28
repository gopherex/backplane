# backplane — design v2

Статус: черновик 2026-09-27 после ревью. Единственный документ дизайна;
прежние спека и docs/ удалены.

## 0. Что такое backplane

Стек без backplane полноценен: Consul, Envoy, NATS, Temporal, PostgreSQL.
backplane ничего из этого не заменяет, не оборачивает и не деплоит.

backplane — место, где **независимые сервисы соединяются в установку**.
Сервис объявляет, что ему *нужно*, что он *умеет*, что *отправляет* и как
*настраивается*; кто с кем соединён и с какими значениями — решает
установка в консоли, а не код. Сервисы не импортируют друг друга:
хуки и активити развязаны биндингом; для событий подписчик знает схему
события (своя копия типа, дескриптор из манифеста или динамически) —
пакет эмиттера ему не нужен.

Что даёт backplane, чего нет в голом стеке:

1. **Развязка** — хуки, активити, биндинги, правила (§7, §8).
2. **Одно место** — карточка каждого сервиса и его UI в одной консоли (§11).
3. **Конфиг с историей** — форма (если есть схема), валидация, ревизии,
   откат над Consul KV (§5).
4. **Каталог** — всё объявленное всеми сервисами, из манифестов (§4).

| компонент | владеет | backplane |
|---|---|---|
| Consul | каталог живых инстансов, KV | читает каталог, манифесты, состояние инстансов; пишет `config/*` |
| Envoy | внешний трафик | конфигурирует по xDS из объявленных роутов |
| NATS | события | наблюдает каталог, lag, DLQ; держит consumers правил |
| Temporal | исполнение: workflows сервисов, активити, биндинги | Nexus-handler и worker биндингов; консоль запусков |
| PostgreSQL | хранилище backplane | биндинги, правила, ревизии, сессии, аудит |
| SDK | один способ написать сервис | контракт |

Правило выбора механизма:

- нужно, чтобы кто-то сделал X, и нужен ответ → **хук** + биндинг (§7);
- произошёл факт, кому интересно — пусть реагируют → **событие**, при
  необходимости с **правилом** (§8);
- чужой сервис, не наш модуль → прямой вызов через Consul DNS (§10).

Принципы:

- **Мы не деплоим.** Кто, где и как запускает процесс — не наше дело.
  Ничего не требует k8s; всё работает в compose и на VM.
- **По адресам ходят только Envoy и консольный прокси backplane** (§11) —
  проброс трафика, адрес из Consul, без состояния. Конфиг — через Consul
  KV, хуки и активити — через очереди Temporal; туда по адресу не ходит
  никто.
- **Сервис не зависит от backplane до последнего момента.** Без backplane
  сервис запускается, конфигурируется из env/файла и работает.
- **Платформа требует имён, не схем.** Хук, активити, событие, ключи
  конфига должны быть объявлены — иначе нечего биндить и показывать.
  Схема (schemapb; protobuf — удобный способ её получить, не обязанность)
  — приложение к манифесту: есть — формы, валидация CEL при сохранении,
  подсветка, маскирование секретов; нет — JSON-редактор и ошибки в
  рантайме. SDK снимает схему автоматически с proto и Go-структур, отказ —
  явный.
- **Два уровня на проводе.** Протокол платформы — protobuf binary, наши
  пакеты `backplane.*`: манифест, состояние инстанса, envelope хука и
  активити, API консоли. Внутри envelope — payload автора как непрозрачный
  JSON: платформа его не типизирует (схемы нет), CEL читает его как `dyn`.
  Внутреннее и внешнее API сервиса платформа не трогает и не декодирует.
- **Один язык выражений — CEL.**

## 1. Имя

**backplane** — объединительная плата (PCB backplane): сервисы вставляются,
она даёт шину и разъёмы, но не решает, что в неё вставят. Go-модуль
`github.com/gopherex/backplane`, proto-пакеты `backplane.*`, env-префикс
`BACKPLANE_`.

## 2. Стек

Все компоненты — внешние процессы, backplane их не запускает.

| | роль | обязателен |
|---|---|---|
| **Consul** | реестр сервисов (каталог, health), KV (конфиг, манифесты, состояние инстансов), DNS | да |
| **Envoy** | единственная точка входа внешнего трафика | да |
| **NATS JetStream** | события | да |
| **Temporal** | workflows сервисов, исполнение хуков, биндингов и правил | да |
| **PostgreSQL** | собственное состояние backplane | да |

Consul — не Kubernetes-специфичен, ставится везде; это и делает возможным
«мы не деплоим». Service mesh не требуется (§10).

## 3. Сервис и его манифест

Сервис — процесс на нашем SDK, со своим API, своими портами, своим кодом.
Всё, что он хочет от платформы, он **объявляет**; SDK собирает объявления
в **манифест** при старте. Семь разделов:

| # | раздел | кто потребляет | обязательно | схема (опционально) |
|---|---|---|---|---|
| 0 | статическая и динамическая конфигурация | консоль, Consul KV | ключи | schemapb из proto / Go-структуры / руками |
| 1 | внешнее API | Envoy (роуты), консоль (только информация) | роуты | OpenAPI или proto-дескрипторы |
| 2 | внутреннее API | его UI-бандл через консольный прокси | полные имена gRPC-сервисов | — (relay байтов) |
| 3 | события, которые он отправляет | подписчики, консоль | имена | schemapb |
| 4 | хуки, которые он вызывает | backplane (биндинги) | имена | schemapb входа/выхода |
| 5 | активити, которые он может обработать | биндинги и правила | имена | schemapb входа/выхода |
| 6 | UI-бандл | консоль | `plugin.json` | — |

Генератор `protoc-gen-backplane` (поверх `protoc-gen-go`,
`protoc-gen-go-temporal`) — удобство: делает объявления и типизированные
обёртки из proto. Runtime SDK принимает объявления в коде с любыми типами.

### 3.0 Конфигурация

Одна структура автора, одна схема. Блок SDK (`config.Backplane`) — её
часть. Поля бывают трёх видов:

- **обычные** — за них отвечает деплой: адреса, порты, креды. Источники —
  env и файл; backplane их не меняет, консоль показывает read-only;
- **`config.Live[T]`** — операционные ручки: фичи, лимиты, уровень логов.
  Меняются из консоли; Consul KV переопределяет **только** их и применяет
  на месте, без рестарта (`Get()`, `Watch(fn)`). Схема помечает их
  аннотацией `backplane.live`, манифест перечисляет пути
  (`config.live`, `greeter.suffix`);
- **`config.Secret`** — маскируется везде: логи, `%v`, JSON, эффективная
  конфигурация в состоянии инстанса, схема (`secret`). Значение —
  `Reveal()`.

Подключение к базе и уровень её логирования живут в одной секции: первое
— обычное поле, второе — `Live`. Секция передаётся компоненту целиком, и
он сам читает живые поля.

### 3.1 Внешнее API

API сервиса для его клиентов. backplane его содержимым не занимается —
Envoy проксирует его до сервиса по роутам, которые сервис объявил:
`{prefix | host, kind, port, schema}`. Схема нужна Envoy для транскодинга и
консоли для вкладки API.

| kind | схема | managed (SDK поднимает сам) | declarative |
|---|---|---|---|
| HTTP | OpenAPI | `svc.HTTP(prefix, handler)` | `route.HTTP(prefix)` |
| gRPC | дескрипторы (из регистрации) | `svc.GRPC(register)` | `route.GRPC(service)` |
| Connect | дескрипторы; Envoy транскодирует gRPC-Web и REST-JSON | `svc.GRPC(register, route.Transcode())` | `route.GRPC(service, route.Transcode())` |
| ws-proto | дескрипторы | `wsproto.Serve(svc, path, route.Origins, register)` | `route.WSProto(path)` |
| GraphQL | интроспекция | `svc.HTTP(path, handler, route.AsGraphQL(introspection))` | `route.GraphQL(path)` |

Managed-роуты по умолчанию делят один публичный порт (`BACKPLANE_PUBLIC_PORT`,
cmux: gRPC и HTTP вместе); `route.Listen(addr)` выносит роут на свой
listener. gRPC и ws-proto принимают одну реализацию: `register` у `GRPC`
и `wsproto.Serve` один и тот же. GraphQL — дело автора: любая библиотека,
его handler, SDK только объявляет роут с интроспекцией. Опции каждой функции —
свой тип: неприменимая опция не компилируется. У ws-proto
политика `route.Origins` — обязательный
аргумент; нулевое значение пускает только same-origin и не-браузерных
клиентов. Регистрация в Consul
получает порт основного публичного listener'а.

### 3.2 Внутреннее API

Обычный gRPC-сервис на **платформенном порту** сервиса (§4) — для его
собственного UI-бандла. Через Envoy не публикуется; в него ходит только
консольный прокси backplane (§11), байты как есть. Другим сервисам
недоступно. Proto-пакет — `<service>.console.v1` (`internal` в пути запрещён Go): полные имена методов
должны быть уникальны в установке, коллизия — манифест отвергается.

### 3.3 События

Именованные сообщения, которые сервис публикует в NATS как CloudEvents с
JSON-payload (§8).

### 3.4 Хуки («нужно»)

Операции, которые сервис **вызывает**, не имея реализации:
`iam.SendEmail(to, template, data) → {message_id}`. Кто и как ответит —
биндинг в backplane (§7). `hook.Call(ctx, req)` работает из любого кода —
HTTP-handler'а, workflow, чего угодно; путь выбирает SDK (§7.2). Хук
помечается `required`, если без биндинга сервис работать не может.

### 3.5 Активити («умею»)

Операции, которые сервис **реализует** и которые могут быть целями чужих
биндингов и правил: `template.Exec(name, data) → {subject, text}`,
`smtp.Send(to, subject, text) → {id}`. Temporal activities или workflows на
очереди сервиса; вход и выход — JSON, SDK декодирует в тип автора.

### 3.6 UI-бандл

Module Federation remote, собранный против UI SDK; `plugin.json` с
`sdk_major` и навигацией. Работает с внутренним API (§3.2) через консоль.
Доставка — §11.2.

## 4. Регистрация, порты, манифест

### 4.1 Порты

У сервиса два класса портов:

- **платформенный порт** — один (`BACKPLANE_INTERNAL_PORT`, 9400),
  поднимает SDK, всё платформенное через cmux: внутреннее API (gRPC),
  `grpc.health.v1`, HTTP-пробы xprobe (`/healthz/liveness`,
  `/healthz/readiness`, `/healthz/startup`), `GET /_backplane/ui/*` (бандл).
  Через Envoy не публикуется;
- **порты внешнего API** — сколько и какие решает автор.

### 4.2 Consul

Одно место для всего зарегистрированного:

- **Каталог** — кто жив и где. Регистрация: ID = `<service>-<hostname>`,
  адрес = `BACKPLANE_ADVERTISE`, иначе `POD_IP`, иначе IP hostname'а;
  `Port` = порт внешнего API (без порта, если внешнего API нет). Health
  check — gRPC health на платформенном порту. Регистрирует SDK (compose,
  VM) или деплой (consul-k8s, Nomad) — тогда
  `BACKPLANE_CONSUL_REGISTER=false`, а свой Consul service ID SDK берёт из
  `BACKPLANE_INSTANCE_ID`. Адрес и `Port` читает Envoy (EDS).
- **Манифест** — `backplane/services/<name>/manifests/<version>`,
  `backplane.Manifest` proto binary до 512 KB, по ключу на версию сервиса. Пишет SDK при старте
  (идемпотентно). Консоль и backplane используют манифест **старшей живой
  версии** (версии живых инстансов — из их состояния); ключи версий, у
  которых не осталось инстансов, backplane удаляет. Rolling upgrade и
  откат не моргают и не требуют CAS.
- **Состояние инстанса** — `backplane/services/<name>/instances/<id>`:
  `version`, адрес и платформенный порт, время старта, эффективная
  конфигурация с источником каждого поля (секреты маскированы), применённая `_revision`, ошибка применения. Ключ
  привязан к Consul-сессии инстанса с TTL — умирает вместе с ним. Отсюда
  консоль берёт «что реально применено», backplane — базу для валидации
  override, консольный прокси — адрес и порт для relay.

backplane держит blocking queries на каталог и на префикс
`backplane/services/`. `Meta` регистрации (512 символов на значение) не
используется — всё наше лежит в KV, и внешняя регистрация деплоем ничего
про нас знать не должна.

### 4.3 Блок SDK в конфигурации

Блок `config.Backplane`, встроенный в конфигурацию автора (тег
`json:"backplane"`); env фиксированный для всех сервисов, остальная
конфигурация читается из `<SERVICE>_*` (`HELLO_GREETER_SUFFIX`):

| | |
|---|---|
| `BACKPLANE_CONSUL_ADDR`, `_CONSUL_TOKEN` | реестр и KV; пусто — без Consul |
| `BACKPLANE_CONSUL_REGISTER` | `false` — регистрирует деплой |
| `BACKPLANE_INSTANCE`, `BACKPLANE_ADVERTISE` | id и адрес инстанса, если не по умолчанию |
| `BACKPLANE_INTERNAL_PORT` | платформенный порт (9400) |
| `BACKPLANE_INTERNAL_SECRET` | секрет платформенного порта (§11.1) |
| `BACKPLANE_PUBLIC_PORT` | публичный порт managed-роутов (8080) |
| `BACKPLANE_NATS_URL`, `_NATS_CREDS` | события; пусто — без NATS |
| `BACKPLANE_TEMPORAL_ADDR`, `_TEMPORAL_NS` | хуки, активити, workflows; пусто — без Temporal |
| `BACKPLANE_ENVIRONMENT` | `deployment.environment.name` |
| `BACKPLANE_LOG_LEVEL`, `BACKPLANE_SHUTDOWN_TIMEOUT` | лог (`info`), бюджет остановки (25s) |
| `BACKPLANE_CONFIG_FILE` | файл конфигурации (YAML/JSON) той же формы, что структура |

Телеметрия — стандартные `OTEL_*` (xtrace `contrib/sdk`); сигнал без
endpoint (общего `OTEL_EXPORTER_OTLP_ENDPOINT` или своего
`OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT`) не экспортируется, одна запись в лог.

### 4.4 SDK изнутри

Автор описывает сервис двумя структурами и одним конструктором:

```go
type Config struct {
    config.Backplane `json:"backplane"`
    Greeter greeter.Config `json:"greeter"`
    Store   store.Config   `json:"store"`
}

type State struct {
    backplane.Root[Config]
    Store   deps.Dependency[*store.DB]
    Greeter *greeter.Greeter
}

func NewState(root backplane.Root[Config]) (*State, error) // пишет автор, всё явно

svc, err := backplane.Open(ctx, NewState) // конфиг → Root → NewState
svc.GRPC(svc.State().Greeter.Register, route.Transcode())
err = svc.Run(ctx)
```

**Дерево узлов** (`deps`). `Root` — корень; под ним автор создаёт узлы и
передаёт их друг другу аргументами конструкторов:

- `deps.Component` + `deps.NewComponent(parent, name)` — код автора;
  встраивается в его тип;
- `deps.Dependency[T]` + `deps.NewDependency(parent, provider, opts...)` —
  внешний ресурс. Обязательная держит старт (ретраи с backoff; сервис жив,
  но не ready) и входит в readiness своей пробой; `deps.Optional()` —
  предупреждение и ретраи в фоне;
- `deps.Singleton[T]` + `deps.NewSingleton(parent, factory)` — ленивое
  значение, строится при первом `Get(ctx)`, закрывается на стопе.

Провайдер (`deps.Provider[T]`: `Name`, `Provide`, `Probe`, `Close`) — форма
будущих contrib-модулей (`contrib/pgx`, `contrib/valkey`, `contrib/s3`):
секция конфигурации + `New(*Config) deps.Provider[T]`. Для разового случая
— `deps.Func(fn, deps.WithProbe(...), deps.WithClose(...))`. Имя узла — от
провайдера, `deps.Name` переопределяет.

Каждый узел — `deps.Scope`: свой путь (`greeter/templates`), логгер с
`node=<путь>`, трейсер и метр (`<service>/<путь>`), `Span`, `Go`,
`OnStart`/`OnStop`. Узлы стартуют в порядке создания и
останавливаются в обратном; дерево попадает в манифест (`nodes`).

**Lifecycle.** Один поверх `xshutdown.Manager`; компоненты стартуют по
порядку и останавливаются в обратном в пределах одного бюджета:

`config → telemetry → health → платформенный порт → дерево узлов → публичные порты → Consul presence → serving gate`

Все горутины — через менеджер; ошибка любой останавливает сервис. Сигнал
во время старта — чистая остановка уже поднятого. Health один: пробы
регистрируются по виду; `grpc.health.v1`, HTTP-пробы и Consul-check читают
одно и то же.

**Объявления.** Хуки, события и активити объявляются на `Root` внутри
конструктора (ссылка передаётся компоненту аргументом) или на `Service`
после `Open`. Все серверы инструментированы (otelgrpc, otelhttp), логи
несут trace_id.

**Consul presence.** Сессия с `LockDelay` 1 мс (дефолтные 15 с не дали бы
перезапущенному инстансу опубликовать состояние); ключ, удерживаемый
прошлой инкарнацией того же инстанса, перехватывается — новейший
побеждает.

### 4.5 Старт сервиса

`Open`: загрузить конфигурацию (файл, env; Consul KV для Live-полей, если
Consul задан и доступен — иначе предупреждение и повтор в фоне) → собрать
State конструктором автора. `Run`: телеметрия → health `NOT_SERVING` →
платформенный порт → дерево узлов (обязательные зависимости ждут
готовности) → порты внешнего API → манифест, состояние, регистрация в
Consul (если есть) → health `SERVING`. Без Consul, NATS и Temporal сервис
работает в объёме внешнего API и конфигурации из env/файла.
Невалидная конфигурация и ошибка конструктора — ошибка `Open`.

## 5. Конфигурация

### 5.1 Слои

```
defaults < файл < env < Consul KV config/<service>/<путь Live-поля>
└─────── деплой ────────┘  └── backplane (только Live) ──┘
```

KV — верхний, необязательный слой и видит только пути Live-полей: ключ
обычного поля в KV игнорируется. Появился — применился горячо, пропал —
остались значения из env/файла.

### 5.2 Роль backplane

Consul KV — **канал доставки**, не источник правды: у KV нет истории,
аудита и отката, а правки через Consul UI реплицируются мгновенно без
следа (норма зрелых установок — в KV пишет автоматика, не человек).

- Источник правды override-слоя — **PostgreSQL backplane**: ревизии
  (значения, автор, комментарий, время), откат = новая ревизия.
- Валидация **до** сохранения: override накладывается на эффективный
  конфигурацию каждого живого инстанса (из состояния инстанса, §4.2) и
  проверяется по схеме, если она есть (schemapb, включая CEL между
  полями); нет живых инстансов — на defaults схемы. Без схемы —
  проверяется только, что это JSON и что ключи объявлены. Невалидное не
  сохраняется.
- Консоль показывает поле по каждому инстансу: эффективное значение и его
  источник (default / файл / env / KV), применённую ревизию и ошибку
  применения — из состояния инстанса.
- Сервис читает KV любым способом (in-process watch через SDK,
  consul-template, envconsul) и о backplane не знает.

### 5.3 Репликация PostgreSQL → Consul

Consul может умереть или потерять данные — мы не теряем ничего:

- сохранение ревизии = транзакция в PostgreSQL, затем запись значений в
  `config/<service>/` одной `txn`-операцией вместе с
  `backplane/services/<service>/config_revision = <n>`;
- **reconciler** в backplane: при старте, по таймеру и по blocking query на
  префикс `config/` сверяет `config_revision` в KV с текущей ревизией в
  PostgreSQL и при расхождении переписывает KV из PostgreSQL. Пустой KV
  восстанавливается за один проход; правки руками в Consul UI
  перетираются;
- пока Consul недоступен, сервисы работают на последних значениях или на
  env/файле; backplane копит ревизии и доставляет, когда Consul вернётся.
  Исключение — обязательное Live-поле без default, которое есть только в
  KV: оно держит старт сервиса, пока Consul не ответит (или не истечёт ctx
  `Open`).

### 5.4 GitOps

Обычные поля остаются в git → Argo → env; их изменение — rollout, и это
правильно. Live-значения живут вне git — это и есть смысл динамической
конфигурации (AWS AppConfig, OpenFeature). Argo не управляет объектами, в
которые пишет backplane, поэтому self-heal их не откатывает. «UI → коммит
в git → Argo → ConfigMap → SDK следит» возможен позже как второй канал.

## 6. Gateway — Envoy

Envoy — единственный вход. backplane — его control-plane по **xDS**: ADS
(один gRPC bidi-стрим на все ресурсы), Delta-режим,
`envoyproxy/go-control-plane`. Ресурсы:

- **LDS** — listeners: порты, TLS, HTTP connection manager;
- **RDS** — маршруты из роутов, объявленных сервисами (§3.1), плюс роуты
  самой консоли (§11.3);
- **CDS** — cluster на сервис;
- **EDS** — endpoints из каталога Consul: адрес и `Port` живых инстансов.

Балансировка, health checking upstream'ов, retries, timeouts, rate limit,
CORS, TLS, gRPC-Web/Connect-транскодинг, WebSocket — Envoy; backplane
только описывает. Envoy получает адрес backplane и node id в bootstrap.

В установке с Consul Connect вход обычно делает Consul API Gateway; тогда
backplane пишет роуты как config entries вместо xDS — второй драйвер
gateway, не в v0.

## 7. Хуки, активити, биндинги

### 7.1 Биндинг

Биндинг — **реализация хука («нужно»), собранная из активити («умею»)**.
Живёт в backplane, редактируется в консоли, к коду сервисов не относится:

```
iam.SendEmail :=
  render = template.Exec(name: req.template, data: req.data)
  send   = smtp.Send(to: req.to, subject: render.subject, text: render.text)
  return { message_id: send.id }
```

IAM знает только свою `SendEmail`; template и smtp — только свои `Exec` и
`Send`; биндинг знает всех троих. В другой установке `iam.SendEmail`
реализуется через `courier.Send` одним шагом — IAM не меняется.

Правила:

- шаг = один вызов активити; `input` шага и `return` — CEL над `req` и
  выходами предыдущих шагов по имени. Значения — JSON (`dyn` в CEL); при
  наличии схем с обеих сторон выражение проверяется по типам при
  сохранении, иначе ошибка приходит в рантайме читаемой;
- шаги линейные или параллельные (группа без зависимостей); `when` на шаг
  (CEL) — пропустить; ни циклов, ни ветвлений глубже `when`, ни состояния.
  Нужно больше — это сервис;
- `undo: <активити>` на шаге — компенсация при ошибке последующих (сага);
- retry/timeout — на шаг, из биндинга; дефолты платформы;
- нет биндинга: `required` хук → читаемая ошибка `no binding for
  iam.SendEmail` и красный слот в карточке, как только манифест появился;
  иначе — ответ по умолчанию (`{}`), сервис продолжает.

### 7.2 Исполнение — Temporal Nexus

Своего транспорта нет. Хук — **Nexus-операция**: Nexus service = сервис
хуков вызывающего (`iam.Hooks`), операция = хук, endpoint = имя сервиса
(`iam`). Endpoint в реестре Temporal указывает на task queue `backplane`;
backplane — Nexus-handler: находит биндинг, исполняет его как workflow
(шаг = activity или child workflow **по имени** на очереди целевого
сервиса), возвращает результат.

Два уровня данных: **envelope — наш proto** (`backplane.HookCall{hook,
trace, deadline, payload}` на входе Nexus-операции,
`backplane.ActivityCall{activity, trace, payload}` на входе активити,
симметричные `*Result`); **payload — JSON** автора внутри `bytes`. CEL
работает над payload, envelope не видит. Если тип автора — proto, SDK
кладёт protojson — это тот же JSON, читаемый и в Temporal UI.

- Endpoint `<service>` backplane создаёт, **как только видит манифест с
  хуками** — не при сохранении биндинга; вызов без биндинга получает
  `no binding`, а не «endpoint not found».
- Nexus вызывается только из workflow-кода. `hook.Call` внутри workflow —
  Nexus напрямую; вне workflow (HTTP-handler, реактор события) SDK
  стартует короткий workflow на очереди самого сервиса, который делает
  Nexus-вызов и возвращает результат. Автор пишет один `Call`; цена вне
  workflow — лишний hop в миллисекунды.
- Активити — Temporal activities/workflows сервиса на его очереди
  `<service>`; регистрируются SDK по имени из манифеста с входом
  `ActivityCall`; декодирование payload в тип автора — в SDK.

Даром от Temporal: durability (цель лежит — шаг ретраится, сделанные шаги
не переисполняются), компенсации, async-вызов, at-least-once, access
policy endpoint'а, visibility каждого запуска со входом, выходом и шагами.

Цена: Temporal — обязательное ядро; латентность десятки миллисекунд на шаг
(хуки — бизнес-уровень, не hot path); payload до 2 MB.

Ошибки читаемы: `step send: smtp.Send: connection refused`, `no binding
for iam.SendEmail`, `transform failed at step render: <CEL>`.

## 8. События

Тонкая обёртка SDK над брокером; под капотом NATS JetStream. Сервис при
желании берёт из SDK нативный клиент.

- **Стрим на сервис** `bp_<service>`, subjects `bp.<service>.<event>`;
  retention и размер изолированы. Создаётся **идемпотентно любой
  стороной** — эмиттером при старте, подписчиком или backplane при
  создании consumer'а; имя и subjects выводятся из имени сервиса,
  retention — дефолт платформы, эмиттер при старте обновляет на своё.
  Поэтому стрим есть до того, как эмиттер впервые запустился.
- **Событие** — именованное сообщение, payload JSON (proto-тип — как
  protojson); envelope — стандартные заголовки CloudEvents, не наш proto.
  Тип автора — что угодно; SDK сериализует.
  `sent := backplane.Event[MailSent](svc, "mail_sent")`.
- **Метаданные — CloudEvents** (NATS binding, headers `ce-*`): `id`,
  `source` = сервис, `type` = имя события, `time`, `subject` = ключ,
  `datacontenttype = application/json`, `dataschema` = ссылка на схему в
  манифесте, если есть; расширения `instance`, `version`, `traceparent`.
- **Порядок** — внутри subject. Партиций нет.
- **Реакторы**: `backplane.React[UserRegistered](svc, "iam.user_registered",
  handler)` = durable consumer `<service>__<event>`, ack/nak, redelivery,
  `max_deliver` → DLQ. Подписчик декодирует своим типом (копия схемы) или
  динамически. Читать чужие стримы может любой; «подписан на всё» =
  consumer на каждый стрим из каталога манифестов, новые — по мере
  появления. Ограничения доступа, если нужны, — правами NATS-пользователя.
- Интерфейс SDK узкий и брокеро-независимый: publish, durable subscribe,
  ack/nak, replay, DLQ. Kafka под него встаёт; v0 — NATS.
- backplane в data path событий не участвует; наблюдает каталог, lag, DLQ;
  держит consumers правил.

### 8.1 Правила: событие → активити

Правило — **биндинг, у которого источник — событие, а не хук**. Тот же
DSL, тот же исполнитель, та же консоль:

```
on iam.UserRegistered when event.email != "" :=
  render = template.Exec(name: "welcome", data: {name: event.name})
  send   = smtp.Send(to: event.email, subject: render.subject, text: render.text)
```

Отличия от биндинга хука: вход называется `event`, `return` нет.

- На каждое правило backplane держит durable consumer на стриме события
  (`bp_iam`, фильтр `bp.iam.UserRegistered`, имя `backplane__rule_<id>`);
  реплики backplane тянут его совместно.
- На сообщение — `ExecuteWorkflow` того же workflow, что исполняет
  биндинги, с `event` как входом. **Workflow id = `rule/<id>/<ce-id>`**,
  политика «дубликат отклонить»: повторная доставка не создаёт второго
  запуска.
- `when` вычисляется до старта; не подошло — ack без запуска.
- Ack **после старта workflow**: durability с этого момента у Temporal;
  DLQ на уровне NATS правилам не нужен.
- Правила **не упорядочены**, запуски параллельны. Если порядок важен —
  это реактор внутри сервиса, не правило.
- Консоль: на карточке события — правила; на карточке правила — запуски по
  префиксу workflow id; тест на примере события.

## 9. Workflows

Сервис описывает свои workflows и activities как хочет; с proto —
`protoc-gen-go-temporal` даёт стабы и типизированные клиенты. Task queue =
имя сервиса, namespace один. SDK даёт подключение и регистрацию worker'а;
больше ничего своего.

- внутри сервиса — обычный Temporal: retry, таймеры, сигналы, cron
  (Temporal Schedules);
- между сервисами — только через хуки (§7): activity вызывает `hook.Call`,
  реализация — биндинг; чужих очередей и типов сервис не знает;
- из консоли — запустить любой workflow, хук или активити с входом: форма
  по схеме, если есть, иначе JSON; запуски, история, отмена, повтор —
  Temporal API в карточке.

## 10. Прямые вызовы и mesh

Для чужих сервисов, не наших модулей: резолв `courier.service.consul` через
Consul DNS и обычный gRPC/HTTP. Ничего между ними нет; backplane не
участвует. Service mesh (sidecar, intentions, mTLS) не требуется — это
инфраструктура деплоя, её нет вне k8s. Чтобы установке с mesh ничего не
мешало: регистрация в Consul — не наша монополия (§4.2); SDK не резолвит
чужие адреса; backplane не вызывает сервисы по адресу иначе как relay
консоли; в mesh gateway-драйвер — Consul config entries (§6).

## 11. Консоль

Shell — собственный UI backplane. Два слоя:

- **Карточки** — из манифеста и стека, без кода сервиса: инстансы и health
  (Consul), конфигурация по инстансам с источниками (обычные поля
  read-only, Live — редактируемые, с историей, §5), дерево узлов, внешнее API и роуты, хуки с состоянием биндингов
  (красный — `required` без биндинга), активити, события с lag/DLQ и
  правилами, workflows с запусками и расписаниями, аудит, телеметрия
  (§15). Где есть схема — формы; где нет — JSON.
- **Плагины** — UI-бандл сервиса (§3.6) как Module Federation remote,
  совпадение `sdk_major` обязательно. Плагин работает только с внутренним
  API своего сервиса (§3.2) через консольный прокси; клиент —
  сгенерированный `protoc-gen-ws-es` из proto сервиса. Прямого пути
  браузер → сервис нет.

### 11.1 Транспорт — одно ws-proto соединение

Консоль держит одно ws-proto соединение к backplane. На нём:

- **собственный API backplane** — зарегистрированные обработчики wsrpc;
- **всё остальное — relay**: `WithUnknownHandler` по полному имени метода
  находит в манифестах, чьё это внутреннее API, берёт адрес и
  платформенный порт живого инстанса из его состояния в KV (§4.2) и
  пробрасывает кадры как есть
  (`RecvRaw → gRPC → SendRaw`), стриминг включительно. Транскодинга нет,
  backplane содержимое не видит.

Правила relay:

1. пробрасываются только методы, объявленные в манифесте как внутреннее
   API этого сервиса; всё прочее — `PERMISSION_DENIED`;
2. платформенный порт доступен только backplane (сеть); дополнительно
   backplane ставит в metadata `BACKPLANE_INTERNAL_SECRET`, SDK проверяет;
3. входящие от браузера `authorization` и `bp-*` срезаются; backplane
   ставит `bp-console-session`;
4. плагины не различаются: один администратор, один origin.

В mesh backplane — член mesh и ходит через sidecar; relay не меняется.

### 11.2 Плагины — Module Federation 2.0

Схема та же, что у Grafana и Backstage; реализация — Module Federation
2.0 (рантайм `@module-federation/enhanced/runtime`, Rspack или Vite).

- **Shell — MF-хост.** React, роутер, Mantine, UI SDK `@backplane/ui`
  объявлены `shared: singleton`. Плагин их импортирует, не бандлит.
- **Плагин — MF-remote.** `mf-manifest.json` + чанки; экспонирует
  `./Routes` и `./Nav`. Рядом наш `plugin.json`: `sdk_major`, навигация.
- **Загрузка — динамическая и ленивая.** Shell получает список плагинов по
  API backplane, для совместимых делает `registerRemotes`, страницы грузит
  `loadRemote` при переходе на `/s/<service>/...`. `sdk_major` ≠ shell'у —
  карточка сообщает, код не грузится.
- **Единый UI** = один роутер, один layout (плагин рендерит только
  контент), один UI-kit из shared.
- **Доставка бандла — у сервиса.** Бандл встроен в бинарь (`embed`), SDK
  отдаёт его на платформенном порту по `GET /_backplane/ui/<path>`.
  backplane раздаёт со своего origin `/plugins/<service>/<hash>/...`,
  забирая у любого живого инстанса и кэшируя по хэшу из манифеста
  (`Cache-Control: immutable`). Один origin → нет CORS, cookie работают, браузер сервис не
  видит. Нет живого инстанса — плагина нет в навигации, карточка остаётся.

### 11.3 Auth консоли

v0 — один оператор.

- **Admin-токен** — единственная учётка. Bootstrap: `BACKPLANE_ADMIN_TOKEN`
  в env backplane, либо генерируется при первом старте, печатается один
  раз, хранится хэшем в PostgreSQL. Ротация из консоли.
- **Вход** — `POST /auth/login` с токеном → cookie `HttpOnly; Secure;
  SameSite=Strict`. Сессии в PostgreSQL: id, создана, истекает, last seen,
  адрес, user-agent; абсолютный срок 12 ч, idle 1 ч; список и отзыв.
- **`/ws` upgrade** — только с cookie и только если `Origin` входит в
  `BACKPLANE_CONSOLE_ORIGINS` (по умолчанию свой host).
- **Brute force** — лимит на адрес + глобальный backoff;
  `BACKPLANE_TRUSTED_PROXIES` для `X-Forwarded-For` за Envoy.
- **Позже** — вход через identity-сервис по OIDC; сессия остаётся
  абстракцией. Не в v0.

Консоль ходит через Envoy как любой сервис: backplane объявляет свои роуты
(`/`, `/ws`, `/plugins/*`, `/auth/*`). Один адрес на всё. Чтобы консоль не
спорила с приложением за `/`, она живёт на своём host'е (SNI/Host-роутинг
в Envoy: `console.<domain>`) либо на префиксе — выбор установки.

### 11.4 UI SDK — `@backplane/ui`

Один npm-пакет, shared singleton в shell. Мажор пакета = `sdk_major`.

1. **Транспорт** — ws-proto клиент на соединении shell'а;
   `useClient(CourierAdminClient)` — сгенерированный `protoc-gen-ws-es`
   клиент внутреннего API; unary и стриминг; ошибки gRPC читаемы.
2. **Формы** — schemapb TS runtime: `<SchemaForm />` — та же форма, что у
   карточки; без схемы — `<JsonEditor />`.
3. **Навигация** — `definePlugin({ nav, routes })` → `./Nav`, `./Routes`;
   shell монтирует под `/s/<service>`.
4. **Компоненты** — слой над Mantine (паттерны HyperDX): layout, таблицы,
   JSON viewer, статусы Temporal, badges, empty/error; тема shell'а.
5. **Контекст** — `usePluginContext()`: сервис, инстансы, health, ревизия.
6. **Сборка** — `@backplane/ui-build`: пресет Rspack/Vite с MF-remote и
   shared, генерация `plugin.json`; `backplane dev --plugin ./ui` — remote
   с локального dev-сервера внутри настоящей консоли.
7. **Клиент внутреннего API** генерируется шаблоном сервиса
   (`protoc-gen-es` + `protoc-gen-ws-es`); UI SDK про сервисы не знает.

## 12. Хранилище и конфигурация backplane

### 12.1 Хранилище

Consul KV персистентен (Raft, снапшоты), но это не БД: нет истории,
запросов кроме get/list, транзакций сверх `txn` на 64 операции, значение
≤ 512 KB. Temporal и так требует PostgreSQL — backplane использует **тот же
инстанс, свою схему `backplane`**.

| где | что |
|---|---|
| **PostgreSQL**, схема `backplane` | биндинги, правила; ревизии Live-значений; сессии консоли; хэш admin-токена; аудит |
| **Consul KV** | манифесты и состояние инстансов — proto binary (пишет SDK); `config/<service>/` — JSON-значения, доставка Live-значений (пишет backplane) |
| **Temporal** | запуски хуков, биндингов, правил с историей — не дублируются |

backplane горизонтально масштабируется: всё состояние — в PostgreSQL,
Consul и Temporal; реплики равноправны, singleton'ов нет. Reconciler на
нескольких репликах пишет одно и то же — идемпотентно; consumers правил
разделяются NATS; Nexus- и binding-worker'ы — обычные Temporal-воркеры;
Envoy подключается к любой реплике.

### 12.2 Конфигурация backplane

backplane — сервис на своём SDK (§14): весь блок §4.3 плюс своё:

| | |
|---|---|
| `BACKPLANE_PG_DSN` | схема `backplane` |
| `BACKPLANE_XDS_LISTEN` | порт для Envoy |
| `BACKPLANE_CONSOLE_LISTEN` | HTTP консоли (`/`, `/ws`, `/auth`, `/plugins`) — за Envoy |
| `BACKPLANE_ADMIN_TOKEN` | bootstrap консоли (опционально) |
| `BACKPLANE_CONSOLE_HOST` или `_PREFIX`, `_ORIGINS`, `_TRUSTED_PROXIES` | консоль |
| `BACKPLANE_OBS_METRICS_URL`, `_LOGS_URL`, `_TRACES_URL` | observability (опционально) |

## 13. Безопасность (v0)

Модель доверия: одна установка, доверенная внутренняя сеть, один
оператор. Ничего своего — у каждого компонента родной auth, креды выдаёт
деплой в конфигурации.

1. **Границы.** Внешний периметр — Envoy; TLS-терминация там или выше
   (LB, ingress) — решает деплой. Внутренние порты наружу не публикуются.
   Внутри plaintext по умолчанию; каждый клиент умеет TLS через конфигурацию.
2. **Consul ACL.** Токен на сервис: регистрировать себя, читать каталог,
   писать свои манифесты и состояние, читать все манифесты (для «подписан
   на всё»), читать свой `config/<name>/`. Токен
   backplane: читать каталог и `backplane/*`, писать `config/*`. Шаблоны
   политик — в репозитории. В dev без ACL работает.
3. **NATS.** Пользователь на сервис: publish `bp.<name>.>`, subscribe
   `bp.>`, JetStream API на свои consumers. backplane — читать всё, JS API
   для consumers правил. В dev без auth работает.
4. **Temporal.** Один namespace; per-service авторизация в OSS требует
   authorizer-плагина — в v0 нет, доверие внутри namespace явное. Nexus
   endpoint access policy — allowlist namespace.
5. **backplane → платформенный порт сервисов.** Сеть плюс
   `BACKPLANE_INTERNAL_SECRET` в конфигурации обеих сторон.
6. **Секреты.** Поле типа `config.Secret` маскируется везде (лог, JSON,
   состояние инстанса, схема); в карточке и истории — по пометке `secret`
   в схеме, без схемы — предупреждение и показ как есть. Live-значения лежат в
   PostgreSQL и KV открытым текстом под защитой доступа; шифрование at
   rest — не в v0.
7. **Консоль** — §11.3; CSP `script-src 'self'`, без inline.
8. **Хуки** — авторизация = биндинг: вызывается только привязанное.

Не в v0: несколько пользователей, RBAC, mesh/mTLS между сервисами,
per-service auth в Temporal, шифрование at rest.

## 14. Аудит

Принцип: не дублировать то, что стек уже пишет.

| что | где | что видно |
|---|---|---|
| запуск хука, биндинга, правила | Temporal history | вход, выход, шаги, ошибки, время; кто — в memo (`source: iam/<instance>` или `console: <session>`) |
| история Live-значений | ревизии в PostgreSQL | значения, автор, комментарий, время, откат |
| обращения к внешнему API | access-логи Envoy | метод, статус, латентность, клиент |
| события | сам JetStream-стрим | replayable лог с `ce-source`, `ce-time` |

Добавляет backplane — действия оператора и системы: биндинг/правило
создано, изменено, удалено (с diff); ревизия конфига, откат; вход,
неудачный вход, logout, отзыв сессии, ротация admin-токена; вызов
внутреннего API через relay — сервис, метод, сессия, статус, длительность,
**без тел**; ручной запуск из консоли; сервис появился / исчез / сменил
манифест.

Хранение: таблица `audit` в схеме backplane — `id` (ulid), `at`, `actor`,
`kind`, `subject`, `detail jsonb`. Append-only, в той же транзакции, что и
изменение. Партиции по месяцу, retention — настройка (12 месяцев).

**backplane — тоже сервис** на своём SDK: манифест, конфигурация с Live-полями,
внутреннее API, UI (shell), события. Аудит эмитится как событие
`backplane.AuditEntry` в `bp_backplane`: аналитика или SIEM забирают его
как любое событие.

Консоль: вкладка «аудит» на карточке (по subject) и глобальная; экспорт
JSON/CSV.

## 15. Observability

Мы ничего не собираем и не храним. Деплой даёт backplane endpoint'ы
запросов к своему стеку телеметрии — backplane показывает **всё, до чего
дотягивается**. Endpoint'ов нет — вкладки нет.

### 15.1 Сквозной контекст

Все части стека умеют OpenTelemetry; SDK и backplane пропагируют его:

- Envoy — span на входящий запрос, `traceparent` в сервис;
- SDK — OTel из коробки (xtrace): серверы инструментированы, логи с
  `trace_id`, экспорт по стандартным `OTEL_*`;
- Temporal — OTel-интерцепторы: workflow, activity, Nexus-операция — хук
  из IAM → биндинг → activity в smtp = один trace;
- NATS — `traceparent` в CloudEvents;
- backplane — сам сервис: relay, reconciler, правила — span'ы.

**Resource-атрибуты ставит SDK**: `service.name`, `service.instance.id`,
`service.version`, `deployment.environment`. Автор ничего не пишет.

### 15.2 Что показывает карточка

Всё по конвенциям, без настройки: метрики с `service="<name>"` (список
через label API, графики по инстансам; сверху — внешнее API из метрик
Envoy, хуки и правила из метрик Temporal, lag событий из JetStream); логи
по `service`/`instance` с переходом по `trace_id`; трейсы по
`service.name` с waterfall и переходом в логи; explore с предзаполненным
контекстом.

Компоненты — из HyperDX (timeseries, логи, waterfall, stat), адаптированные
под Mantine и UI SDK.

### 15.3 Драйверы и что должен обеспечить деплой

backplane — query-proxy: ходит в endpoint'ы со своей аутентификацией,
подставляет контекст сервиса.

| драйвер | метрики | логи | трейсы |
|---|---|---|---|
| **victoria** (v0) | VictoriaMetrics, PromQL/MetricsQL | VictoriaLogs, LogsQL | VictoriaTraces, Jaeger-совместимый API |
| prometheus/loki/tempo (позже) | PromQL | LogQL | TraceQL |

Панели Envoy, Temporal и NATS показывают что-то, только если деплой
скрейпит их метрики в тот же backend. Мы пишем для девопсов документ:
что скрейпить (Envoy `/stats/prometheus`, Temporal server, NATS exporter),
какие лейблы ждём, куда слать OTLP. Не сделали — панели пустые, остальное
работает.

Не в v0: alerting (возможно v1), редактор дашбордов, панели как данные,
своё хранилище телеметрии.

## 16. Границы v0, milestones, риски

### 16.1 Не-цели v0

Деплой и оркестрация; service mesh; несколько пользователей и RBAC;
alerting; редактор дашбордов; шифрование at rest; per-service auth в
Temporal; Kafka-драйвер; gateway через Consul config entries; вход через
identity-сервис; CaC/CLI/GitOps-канал доставки конфига; партиции событий;
`template upgrade`.

### 16.2 Milestones

**M0 — SDK и `hello`, без backplane.** Platform-in-a-box: compose с
Consul, NATS, Temporal, PostgreSQL, Envoy. SDK: конфигурация из
env/файла с Live-полями из KV, дерево узлов, манифест и состояние инстанса в KV, регистрация в Consul,
платформенный порт (cmux: внутреннее API, health, xprobe, бандл),
OTel-атрибуты. `hello` объявляет все семь разделов по минимуму.
Доказывает: сервис живёт без backplane; манифест и слои конфига верны.

**M1 — backplane: конфиг, gateway, консоль.** Watch каталога и KV; схема
PostgreSQL, ревизии, репликация в KV, hot reload в `hello`; xDS из роутов
`hello`; shell с карточками (инстансы, конфигурация по инстансам с
историей Live-значений, внешнее API); relay ws-proto → внутреннее API; auth консоли.
Доказывает: цикл «форма → ревизия → KV → сервис» и вход через Envoy.

**M2 — хуки, биндинги, правила, события.** Temporal в SDK; Nexus-endpoint
и binding-workflow; DSL с CEL над JSON; события CloudEvents и реакторы;
правила. `hello` объявляет хук `Greet` (вызывается из HTTP-handler'а —
проверка пути вне workflow), активити `Echo` и событие `Greeted`; биндинг
`hello.Greet := hello.Echo`, правило `on hello.Greeted := hello.Echo`.
Доказывает: развязка end-to-end, один trace через всё.

**M3 — плагины, observability, аудит.** UI SDK, MF-хост, доставка бандла,
плагин `hello`; драйвер `victoria`; аудит и событие `backplane.AuditEntry`.

**Дальше, вне репозитория** — Kratos-wrapper, `template`, `smtp` (courier
режется на атомарные способности), биндинг `iam.SendEmail`. Любой special
case в SDK ради них — дефект модели.

### 16.3 Эталонные сервисы, conformance, шаблон

- В репозитории один пример — `examples/hello`; он же и вызывает хук, и
  реализует активити (`hello.Greet := hello.Echo`). Ничего доменного.
- **Conformance** — тесты над контрактом SDK против platform-in-a-box на
  каждом PR: манифест и состояние инстанса в KV; слои конфига и hot reload
  по записи в KV; relay внутреннего API; хук через Nexus из workflow и из
  HTTP-handler'а; активити по имени с JSON-входом; событие с корректными
  `ce-*`; правило запускается и дедуплицируется по `ce-id`; бандл с
  платформенного порта. Другой язык SDK проходит этот же набор.
- **Шаблон сервиса** — один; генерирует `proto/`, `internal/`, `cmd/`,
  `ui/`, Makefile, Dockerfile, conformance-таргет; версия шаблона
  записывается; `template upgrade` — v1.

### 16.4 Известные риски

- **Регистрация Nexus-операций.** В Temporal Go набор операций фиксируется
  до старта worker'а. backplane — один handler для хуков всех сервисов:
  новый хук → пересобрать набор и перезапустить worker (объект в
  процессе). Проверить на M2 первым: нет ли catch-all и не рвёт ли
  перезапуск in-flight.
- **Активити по имени.** Единственное место, где backplane формирует
  данные для чужого кода: envelope `ActivityCall` наш, payload — JSON из
  CEL; conformance проверяет первым.
- **HyperDX-компоненты** не публикуются как библиотека — извлечение и
  адаптация под Mantine, работа на M3.

## 17. Конвенции имён

| что | как |
|---|---|
| имя сервиса | `[a-z0-9-]+`, уникально в установке |
| id инстанса | `<service>-<hostname>` (или `BACKPLANE_INSTANCE_ID`) = Consul service ID = `service.instance.id` |
| Consul: регистрация | адрес `BACKPLANE_ADVERTISE` / `POD_IP` / hostname; `Port` внешнего API; `Meta` не используется |
| Consul KV | `backplane/services/<service>/manifests/<version>`, `backplane/services/<service>/instances/<id>`, `config/<service>/<path>` — только пути Live-полей (вложенность — `/`; скаляр — строкой, контейнер — JSON); ревизия — `backplane/services/<service>/config_revision`, вне префикса значений |
| env | `<SERVICE>_<PATH>`, путь — верхний регистр, `_` между уровнями; блок SDK — `BACKPLANE_<PATH>` |
| файл | `BACKPLANE_CONFIG_FILE` (YAML/JSON) той же формы, что структура конфигурации |
| блок SDK | `BACKPLANE_*` — таблица §4.3 |
| NATS | стрим `bp_<service>`, subject `bp.<service>.<event>`, consumer `<subscriber>__<event>`, правила `backplane__rule_<id>` |
| Temporal | namespace один; task queue `<service>`; очередь backplane `backplane`; Nexus endpoint `<service>`, Nexus service `<service>.Hooks`; workflow id правила `rule/<id>/<ce-id>` |
| имена хуков, активити, событий | `<service>.<Name>`, `Name` — CamelCase |
| proto-пакеты | внутреннее API `<service>.console.v1`; хуки `<service>.hooks.v1`; активити `<service>.activities.v1`; события `<service>.events.v1` |
| консоль | `/s/<service>/...` — плагин; `/plugins/<service>/<hash>/...` — бандл |

## 18. Раскладка репозитория

```
backplane/
  platform-design.md
  Makefile  easyp.yaml  go.mod
  docker-compose.yaml        platform-in-a-box: Consul, Envoy, NATS, Temporal, PostgreSQL, backplane
  deployments/               всё про деплой: конфиги Envoy bootstrap, Consul, Temporal, NATS;
                             документ для девопсов (§15.3); k8s-примеры позже
  backplanepb/               proto-исходники backplane.v1 и сгенерированный Go
  pkg/backplane/             Go SDK: Open, Root, Service
    build/ config/ route/    идентичность из ldflags, конфигурация (Live, Secret), роуты
    deps/                    дерево узлов: Component, Dependency, Singleton, Provider
    wsproto/                 ws-proto поверх Service.HTTP (своя зависимость)
    hook/ activity/ event/   объявления хуков, активити, событий (транспорт — M2)
    internal/                lifecycle, tree, health, listener, manifest, consul, guard, telemetry
  internal/                  приватное backplane: registry (Consul watch), config (PG ↔ KV),
                             xds, nexus (handler и binding-workflow), rules, console (ws-proto
                             server, relay, auth), store (PostgreSQL), obs (query-proxy)
  cmd/
    backplane/               бинарь платформы
    protoc-gen-backplane/    генератор (удобство)
  examples/
    hello/                   единственный пример: proto/, internal/, cmd/, ui/
  web/                       yarn workspace
    packages/console/        shell (MF-хост)
    packages/ui/             @backplane/ui — UI SDK для плагинов, включая @backplane/ui-build
  conformance/               тесты контракта SDK против platform-in-a-box
```

`pkg/` — только то, что импортируют сервисы; всё остальное Go — в
`internal/`. Корень — документ, сборка, compose.

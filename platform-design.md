# backplane — design v2

Статус: черновик. Единственный документ дизайна платформы и её Go SDK.

**Что из этого есть в коде.** Репозиторий содержит Go SDK
(`pkg/backplane`, proto-контракт `backplanepb`), эталонный сервис
`examples/hello`, conformance-тесты и platform-in-a-box
(`docker-compose.yaml`). Сервера backplane (бинарь `cmd/backplane`,
консоль, UI SDK, генератор `protoc-gen-backplane`) в репозитории нет:
разделы и абзацы, помеченные **[backplane]**, — дизайн сервера платформы,
а не поведение SDK. Всё непомеченное про SDK описывает код как он есть;
статус по этапам — §16.2.

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
  рантайме. SDK снимает схему автоматически с proto и Go-структур: для
  конфигурации схема, которая не снимается, — ошибка `Open`, для хуков, активити,
  событий и workflows — объявление без схемы (payload — просто JSON).
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

«Обязателен» — для установки с backplane. Сервис на SDK стартует и
работает без любого из них: недостающее — предупреждение в лог, не ошибка
(§4.5).

Consul — не Kubernetes-специфичен, ставится везде; это и делает возможным
«мы не деплоим». Service mesh не требуется (§10).

## 3. Сервис и его манифест

Сервис — процесс на нашем SDK, со своим API, своими портами, своим кодом.
Всё, что он хочет от платформы, он **объявляет**; SDK собирает объявления
в **манифест** (`backplane.v1.Manifest`, §4.2) в `Run`. Восемь разделов:

| # | раздел | кто потребляет | обязательно | схема (опционально) |
|---|---|---|---|---|
| 0 | конфигурация: обычные и Live-поля | консоль, Consul KV | ключи | schemapb из Go-структуры |
| 1 | внешнее API | Envoy (роуты), консоль (только информация) | роуты | OpenAPI или proto-дескрипторы |
| 2 | внутреннее API | его UI-бандл через консольный прокси | полные имена gRPC-сервисов | — (relay байтов) |
| 3 | события, которые он отправляет, и реакторы на чужие | подписчики, консоль | имена | schemapb |
| 4 | хуки, которые он вызывает | backplane (биндинги) | имена | schemapb входа/выхода |
| 5 | активити, которые он может обработать | биндинги и правила | имена | schemapb входа/выхода |
| 6 | UI-бандл | консоль | `plugin.json` | — |
| 7 | свои workflows и расписания (§9) | консоль | имена | schemapb входа/выхода |

Плюс то, что SDK добавляет сам: дерево узлов автора (`nodes`) и общий
`FileDescriptorSet` сервисов (`descriptors`).

Объявления — в коде, с любыми Go-типами. Генератор
`protoc-gen-backplane` (поверх `protoc-gen-go`, `protoc-gen-go-temporal`),
который делал бы объявления и типизированные обёртки из proto, — удобство
на будущее: его нет, и SDK от него не зависит.

### 3.0 Конфигурация

Одна структура автора, одна схема. Блок SDK (`config.Backplane`) — её
часть. Поля бывают трёх видов:

- **обычные** — за них отвечает деплой: адреса, порты, креды. Источники —
  env и файл; backplane их не меняет, консоль показывает read-only;
- **`config.Live[T]`** — операционные ручки: фичи, лимиты, уровень логов.
  Меняются из консоли; Consul KV переопределяет **только** их и применяет
  на месте, без рестарта. `Get()` — текущее значение; `Watch(fn)` —
  колбэк на каждое новое значение после загрузки (по порядку, в горутине
  обновления конфигурации; паника колбэка логируется и не роняет сервис);
  `config.LiveOf(v)` — Live со значением для дефолтов в коде и тестов;
  нулевой Live читает нулевое значение и никогда не срабатывает. Все копии
  секции делят одно текущее значение. JSON (`MarshalJSON`) — текущее
  значение. Live-поля живут только в структурах: Live внутри слайса,
  массива или map — ошибка `Open`. Схема помечает их аннотацией
  `backplane.live`, манифест перечисляет пути (`config.live`,
  `greeter.suffix`);
- **`config.Secret`** — маскируется везде: `***` для любого fmt-глагола
  (`%v`, `%s`, `%d`, `%q`, `%x`, `%+v`, `%#v`), `MarshalText` (slog, YAML),
  JSON, эффективная конфигурация в состоянии инстанса, схема (`secret`).
  Значение — `Reveal()`. fmt не видит Secret в неэкспортированных полях
  при печати структуры через `%v`: такие поля экспортируются или структура
  не печатается.

Подключение к базе и уровень её логирования живут в одной секции: первое
— обычное поле, второе — `Live`. Секция передаётся компоненту целиком
(указателем на секцию из `Root.Config()`), и он сам читает живые поля.

Проверки сверх схемы — метод `Validate() error` на конфигурации или
любой секции (`config.Validator`): ошибка — отказ `Open`, а для
обновления из KV — отказ в его применении целиком (§5.5).

Пакет `config` работает и без остального SDK: `config.Load[C](ctx, opts...)`
читает один раз (defaults, файл, env) — для утилит и тестов;
`config.Open[C](ctx, opts...)` (C встраивает `config.Backplane`)
возвращает `*config.Runtime[C]` — живую конфигурацию: `Value()`
(стабильный указатель, Live-поля обновляются на месте), `Degraded()`
(почему слой Consul пуст или устарел), `Close()`. Опции: `Service`
(имя, если `build.Service` не проштампован), `File` (вместо
`BACKPLANE_CONFIG_FILE`; YAML или JSON по расширению), `WithoutFile`,
`EnvPrefix` (вместо `<SERVICE>_`), `WithoutEnv`, `Source` (свои слои выше
env, ниже KV), `WithoutConsul`, `ConsulOptions` (в Consul-источник xconf),
`ConsulBackoff(min, max)` (пауза между попытками до недоступного Consul,
по умолчанию 1s..30s). `ctx` ограничивает только загрузку; runtime живёт
до `Close`.

Env автора — `<SERVICE>_<PATH>`: префикс — имя сервиса в верхнем
регистре, `-` и `.` → `_` (`my-svc` → `MY_SVC_`); путь — JSON-имена
полей в верхнем регистре через `_` (`HELLO_GREETER_SUFFIX`). Блок SDK
читается из фиксированного `BACKPLANE_<PATH>` (§4.3); `<SERVICE>_*` —
слой выше, так что `<SERVICE>_BACKPLANE_<PATH>` перекрывает
`BACKPLANE_<PATH>`. Объект или список задаётся одной переменной целиком
в JSON (`BACKPLANE_CONSUL_TAGS='["blue"]'`); объект целиком и его поле по
отдельности вместе — ошибка загрузки.

`config.TLS` — TLS клиентского соединения: `Enabled`, `CA`, `Cert`, `Key`
(`Secret`), `ServerName`, `InsecureSkipVerify`; PEM-содержимое, не пути.
`ClientConfig()` строит из него `*tls.Config` (TLS 1.2+; `nil` при
выключенном TLS; пустой `CA` — системный пул; `Cert` и `Key` — только
вместе, иначе `config.ErrTLSPair`; `CA` без сертификата —
`config.ErrTLSCA`) — это тот же `*tls.Config`, что SDK строит для
Consul, NATS и Temporal.

### 3.1 Внешнее API

API сервиса для его клиентов. backplane его содержимым не занимается —
Envoy проксирует его до сервиса по роутам, которые сервис объявил:
`{prefix, host, kind, port, schema, policy}`. Схема нужна Envoy для
транскодинга и консоли для вкладки API, политика — Envoy (§6).

| kind | схема | managed (SDK поднимает сам) | declarative (`svc.Route(decl)`) |
|---|---|---|---|
| HTTP | OpenAPI (`route.OpenAPI(spec)`) | `svc.HTTP(prefix, handler, opts...)` | `route.HTTP(prefix, opts...)` |
| gRPC | дескрипторы (из регистрации) | `svc.GRPC(register, opts...)` | `route.GRPC(service, route.Descriptors(fds))` |
| Connect | дескрипторы; Envoy транскодирует gRPC-Web и REST-JSON | `svc.GRPC(register, route.Transcode())` | `route.GRPC(service, route.Transcode())` |
| ws-proto | дескрипторы (из регистрации) | `wsproto.Serve(svc, prefix, origins, register, opts...)` | `route.WSProto(prefix, descriptors)` |
| GraphQL | интроспекция (JSON) | `svc.GraphQL(prefix, handler, introspection, opts...)` | `route.GraphQL(prefix, introspection)` |

Managed-роуты по умолчанию делят один публичный порт (`BACKPLANE_PUBLIC_PORT`,
cmux: gRPC и HTTP вместе); `route.Listen(addr)` выносит роут на свой
listener, роуты с одним адресом делят один сервер. Роут матчится по
префиксу и, с `route.Host(h)`, ещё и по Host: точному
(`api.example.com`) или wildcard (`*.example.com`); один префикс на
одном порту может обслуживать несколько хостов — Go выбирает handler так
же, как Envoy (точный хост, затем самый длинный wildcard, затем роут без
хоста; не совпало — 404). У declarative-роутов `route.Port(p)` — порт, на
котором их обслуживает автор (0 — порт из регистрации в Consul). Опции
каждой функции — свой интерфейс (`GRPCOption`, `HTTPOption`,
`WSProtoOption`, `GRPCDeclOption`, `HTTPDeclOption`, `DeclOption`):
неприменимая опция не компилируется.

Политика Envoy — опции любого роута, managed и declarative:
`route.Timeout(d)` (весь запрос, стрим тоже; стриминговому роуту её
задают явно — у Envoy по умолчанию 15s; у ws-proto по умолчанию её нет,
§6), `route.IdleTimeout(d)`,
`route.Retry(attempts, perTry, on...)` (`on` — условия `retry_on` Envoy:
`5xx`, `reset`, `unavailable`, ...), `route.CORS(route.CORSPolicy{Origins,
Methods, Headers, ExposeHeaders, Credentials, MaxAge})`,
`route.MaxRequestBytes(n)`. Она попадает в `Route.policy` манифеста; SDK её
не исполняет. Незаданное поле — умолчание платформы; отрицательная
длительность, `Retry` без попыток и CORS без origin — ошибка `Run`.

Опции сервера у managed-роутов: `route.Interceptors(...)` и
`route.StreamInterceptors(...)` (у `svc.GRPC` и `wsproto.Serve`)
оборачивают вызовы только сервисов этой регистрации — один gRPC-сервер
порта общий, SDK выбирает цепочку по имени сервиса вызова; первый
перехватчик — внешний. `route.Middleware(mw)` (у `svc.HTTP`,
`svc.GraphQL`, `wsproto.Serve` — там он видит upgrade-запрос)
оборачивает handler этого роута, первый — внешний. `route.Reflection()`
у `svc.GRPC` включает `grpc.reflection` на порту роута (один раз на
порт); Envoy о ней не знает — она отвечает на порту напрямую.
`backplane.GRPCServerOptions(opts...)` (опция `Open`) передаётся каждому
публичному gRPC-серверу после опций SDK: лимит автора перекрывает лимит
SDK, его перехватчики работают внутри recovery и снаружи
`route.Interceptors`. Каждый публичный gRPC-сервер обслуживает
`grpc.health.v1` с тем же статусом, что платформенный порт, — сервис
`grpc.health.v1` автор сам не регистрирует (повтор — ошибка `Run`).

Паника handler'а — ошибка одного запроса, а не падение процесса: gRPC и
ws-proto отвечают `INTERNAL`, HTTP — 500 (если ответ ещё не начат;
`http.ErrAbortHandler` сохраняет смысл). Каждая паника пишется в лог со
стеком и trace_id запроса и считается в `backplane.panics{where}`
(`grpc.public`, `grpc.internal`, `wsproto`, `http.public`,
`http.platform`). Recovery стоит первым в цепочке и покрывает все
перехватчики после себя.

HTTP-префикс нормализуется к `/` в конце: `"/api"` обслуживает
`/api/...` и в Go, и в Envoy. Один и тот же хост с префиксом дважды на
одном порту или один gRPC-сервис, зарегистрированный дважды на одном
сервере, — ошибка `Run`, не падение процесса. gRPC и ws-proto принимают одну реализацию: `register`
у `GRPC` и `wsproto.Serve` один и тот же. Managed-роуты gRPC, Connect и
ws-proto несут в манифесте полные имена сервисов (`services`), а их
дескрипторы вместе с транзитивными импортами лежат один раз в
`Manifest.descriptors`, общем для всех роутов и внутреннего API; роут со
своей схемой (declarative) несёт её сам. GraphQL — дело автора: любая
библиотека, его handler, SDK объявляет роут с интроспекцией (`nil` — без
схемы).

У ws-proto политика origin (`route.Origins`) — обязательный аргумент.
Нулевое значение `route.Origins{}` пускает только same-origin браузеры и
не-браузерных клиентов; `route.AllowOrigins("app.example.com",
"*.example.com")` добавляет перечисленные хосты; `route.AnyOrigin()` —
любой origin, для эндпоинтов, которые аутентифицируют каждый вызов сами и
не полагаются на cookie. Регистрация в Consul получает порт основного
публичного listener'а: общего публичного порта, если он используется,
иначе первого объявленного.

### 3.2 Внутреннее API

Обычный gRPC-сервис на **платформенном порту** сервиса (§4) — для его
собственного UI-бандла. Через Envoy не публикуется; в него ходит только
консольный прокси backplane (§11), байты как есть. Другим сервисам
недоступно. Регистрация — `svc.Internal(register)`. Proto-пакет —
`<service>.console.v1` (`-` в имени сервиса → `_`; `internal` в пути
запрещён Go): сервис из другого пакета — ошибка `Run`. **[backplane]**
Полные имена методов должны быть уникальны в установке: манифест с
коллизией backplane отвергает.

Внутреннее API закрыто секретом (`BACKPLANE_INTERNAL_SECRET`, §11.1) и
обслуживается только пока поднято дерево автора: до его старта (например,
пока обязательная зависимость ещё ретраится) вызовы получают
`UNAVAILABLE`, а на остановке SDK закрывает приём и ждёт вызовов в полёте
до того, как дерево и его зависимости остановятся. `grpc.health.v1` на
том же сервере не закрыт ни секретом, ни этим гейтом.

### 3.3 События

Именованные сообщения, которые сервис публикует в NATS как CloudEvents с
JSON-payload (§8). `event.Declare[T](scope, "Greeted", event.Describe(s))`
возвращает `event.Ref[T]`; `ref.Publish(ctx, v, opts...)` отправляет
(опции — `event.Key`, `ID`, `Time`, `Header`, §8), `ref.Name()` — полное
имя `<service>.Greeted`. Реактор на событие любого сервиса —
`event.React[T](scope, "iam.UserRegistered", fn, opts...)` (опции
доставки — `event.MaxDeliver`, `Concurrency`, `Ordered`, `Timeout`,
`Redelivery`, `StartAt`, `Consumer`, `InactiveThreshold`, §8); в манифесте
он — `subscriptions` (`event` и `consumer`). Метаданные доставки —
`event.DeliveryOf(ctx)`, ошибка без повторов — `event.Terminal(err)`,
повторная обработка dead letters — `event.Redrive`, нативный клиент —
`event.JetStream(scope)`.

### 3.4 Хуки («нужно»)

Операции, которые сервис **вызывает**, не имея реализации:
`iam.SendEmail(to, template, data) → {message_id}`. Кто и как ответит —
биндинг в backplane (§7).

```go
send := hook.Declare[Email, Sent](root, "SendEmail", hook.Required(),
    hook.DefaultTimeout(10*time.Second), hook.Describe("письмо пользователю"))

out, err := send.Call(ctx, in, hook.Key(in.RequestID), hook.Timeout(5*time.Second))
```

- `hook.Declare[Req, Res](scope, name, opts...)` возвращает
  `hook.Ref[Req, Res]`. Опции: `hook.Required()` — без биндинга сервис
  работать не может; `hook.DefaultTimeout(d)` — дедлайн вызова, если сам
  вызов его не задал (`d ≤ 0` игнорируется); `hook.Describe(s)` —
  описание для консоли. Манифест несёт `required`, `timeout`,
  `description` и схемы. `ref.Name()` — полное имя `<service>.<Name>`.
- `ref.Call(ctx, req, opts...)` работает из любого Go-кода — HTTP-handler'а,
  реактора, активити, `OnStart` и `OnStop` компонента (§7.2); из
  workflow-кода — `ref.WorkflowCall(wctx, req, opts...)` с
  `workflow.Context`. Пока транспорта нет (Temporal не задан, не
  подключён или backplane ещё не завёл Nexus endpoint сервиса), `Call`
  сразу возвращает `hook.ErrUnavailable`; нет биндинга —
  `hook.ErrNoBinding`.
- Дедлайн вызова — самый ранний из дедлайна `ctx` и собственного:
  `hook.Timeout(d)` вызова, иначе `DefaultTimeout` объявления, иначе — если
  у `ctx` дедлайна нет — платформенный `backplane.temporal.hook_timeout`
  (30 s, §4.3). У `WorkflowCall` дедлайн есть всегда: тот же выбор, урезанный
  до остатка таймаута run'а; бесконечного вызова хука не бывает.
- `hook.Key(k)` — идемпотентный вызов: вызовы с одним ключом исполняют
  биндинг один раз. Вызов, пока первый идёт, ждёт его; вызов после
  успешного — получает его результат, не исполняя заново (вход повторного
  вызова не смотрится); вызов после неуспешного — исполняется снова. Ключ
  живёт, пока Temporal хранит завершённый запуск (retention namespace'а).
  `WorkflowCall` ключ игнорирует — workflow и так durable.
- Метрика `backplane.hook.call.duration` (атрибуты `hook`, `outcome`:
  `ok`, `no_binding`, `unavailable`, `timeout`, `error`) — на каждый `Call`
  (`WorkflowCall` виден в истории workflow и её не пишет).

### 3.5 Активити («умею»)

Операции, которые сервис **реализует** и которые могут быть целями чужих
биндингов и правил: `template.Exec(name, data) → {subject, text}`,
`smtp.Send(to, subject, text) → {id}`. Temporal activities или workflows на
очереди сервиса; вход и выход — JSON, SDK декодирует в тип автора.
Объявление вместе с реализацией:

```go
activity.Handle(root, "Send", smtp.Send, activity.StartToClose(10*time.Second),
    activity.HeartbeatTimeout(5*time.Second), activity.Retry(activity.RetryHint{Attempts: 5}),
    activity.Describe("отправка письма"))
activity.Workflow(root, "Onboard", onboarding.Run) // func(workflow.Context, Req) (Res, error)
```

- `activity.Handle(scope, name, func(ctx, in Req) (Res, error), opts...)` —
  Temporal activity (`kind: ACTIVITY` в манифесте). Ошибка обработчика
  повторяется по политике шага; `activity.NonRetryable(err)` — без
  повторов; вход, который не декодируется, — тоже без повторов (§7.2).
- `activity.Workflow(scope, name, func(wctx, in Req) (Res, error), opts...)`
  — активити на workflow (`kind: WORKFLOW`): шаг биндинга исполняет её как
  child workflow с этим именем на очереди сервиса; внутри можно ждать,
  спать, вызывать хуки (`WorkflowCall`) и свои активити. Ошибки — как у
  `Handle` (§7.2).
- Опции — умолчания для шага биндинга, который её вызывает (биндинг может
  переопределить; SDK их не навязывает): `StartToClose(d)` — одна попытка
  (для `Workflow` — один run), `HeartbeatTimeout(d)` — попытка без
  heartbeat дольше `d` считается потерянной, `Retry(RetryHint{Attempts})` —
  сколько попыток всего, `Describe(s)` — описание. В манифесте —
  `start_to_close`, `heartbeat`, `retry.attempts`, `description`.
- `activity.InfoOf(ctx) (Info, bool)` в обработчике: `Attempt` (1 —
  первая), `Binding` и `Step` (из `ActivityCall`: кто вызвал и какой шаг),
  `Key` — `<workflow id>/<activity id>`, одинаковый у всех попыток одного
  исполнения шага (ключ идемпотентности для внешних побочных эффектов),
  `Deadline` попытки. Обработчик, вызванный напрямую, а не транспортом или
  `backplanetest.Activity` (§4.6), получает `false`.
- `activity.Heartbeat(ctx, details...)` — прогресс долгого обработчика:
  таймаут heartbeat отсчитывается от последнего, отмену шага `ctx`
  замечает на следующем; вне транспорта ничего не делает.

Хуки, события, реакторы и активити объявляются на любом узле дерева
(`deps.Scope`: `Root`, компонент) до `Run`: объявление находит свой сервис
через узел, глобального реестра нет. Имена хуков, активити и workflows
(§9) — CamelCase, `[A-Z][A-Za-z0-9]*` (`SendEmail`); другое имя — паника
при объявлении с понятным сообщением: это имена типов Temporal и операций
Nexus. Схемы входа и выхода снимаются с Go-типов (schemapb); не снялась —
объявление остаётся, payload — просто JSON (protojson для
proto-сообщений). Вход и выход меняются только аддитивно: читатель
игнорирует незнакомые поля, отсутствующее поле читается нулевым значением,
переименование поля — это новое поле рядом со старым, пока все читатели не
перейдут. Имя на проводе — `<service>.<Name>`.

### 3.6 UI-бандл

Module Federation remote, собранный против UI SDK; `plugin.json` с
`sdk_major` и навигацией. Работает с внутренним API (§3.2) через консоль.
Объявление — `svc.UI(bundle fs.FS)`; манифест получает `ui.hash` (sha256
по пути и содержимому каждого файла бандла) и `ui.sdk_major` из
`plugin.json`; бандл без читаемого `plugin.json` — ошибка `Run`. SDK
отдаёт бандл на платформенном порту (`GET /_backplane/ui/<path>`, за
секретом, §4.1) с `ETag` = `"<ui.hash>"` и `Cache-Control: no-cache` на
каждом файле: кэш перепроверяет файл и получает `304`, пока бандл не
изменился. Доставка в браузер — §11.2.

## 4. Регистрация, порты, манифест

### 4.1 Порты

У сервиса два класса портов:

- **платформенный порт** — один (`BACKPLANE_INTERNAL_PORT`, 9400),
  поднимает SDK, всё платформенное через cmux: внутреннее API (gRPC),
  `grpc.health.v1`, HTTP-пробы xprobe (`/healthz/liveness`,
  `/healthz/readiness`, `/healthz/startup`), `GET /_backplane/ui/*` (бандл,
  §3.6), `GET /_backplane/info` (JSON: `service`, `version`, `instance`,
  `advertise`, `environment`, `sdk_version`, `go_version`, `commit`,
  `build_date`) и, с `BACKPLANE_PPROF=true`, `/debug/pprof/*`. Пробы и
  `grpc.health.v1` открыты; остальное — только с секретом (§11.1). Через
  Envoy не публикуется;
- **порты внешнего API** — общий публичный порт managed-роутов
  (`BACKPLANE_PUBLIC_PORT`, 8080), отдельные `route.Listen(addr)` и порты,
  которые автор обслуживает сам (declarative-роуты).

Пробы автора — `svc.LivenessProbe(p)`, `svc.ReadinessProbe(p)`,
`svc.StartupProbe(p)` (xprobe). Обязательные зависимости входят в
readiness сами; в liveness зависимостям не место — мёртвая база не повод
перезапускать процесс.

### 4.2 Consul

Одно место для всего зарегистрированного:

- **Каталог** — кто жив и где. Регистрация: ID = id инстанса
  (`BACKPLANE_INSTANCE`, иначе `<service>-<hostname>`),
  адрес = `BACKPLANE_ADVERTISE`, иначе `POD_IP`, иначе IP hostname'а;
  `Port` = порт внешнего API (без порта, если внешнего API нет); `Tags` —
  `BACKPLANE_CONSUL_TAGS`. Health check — gRPC health на платформенном
  порту: интервал `_CHECK_INTERVAL` (10s), таймаут `_CHECK_TIMEOUT` (5s),
  снятие после `_DEREGISTER_AFTER` (1m) в critical. В каталог инстанс
  попадает только когда обслуживает трафик (узел `register`, §4.4) и
  уходит из него первым на остановке; пока зависимости ретраятся, он виден
  только по состоянию (фаза `starting`). Регистрирует SDK (compose,
  VM) или деплой (consul-k8s, Nomad) — тогда
  `BACKPLANE_CONSUL_REGISTER=false`, а свой Consul service ID SDK берёт из
  `BACKPLANE_INSTANCE`. Адрес и `Port` читает Envoy (EDS). Адрес по
  умолчанию — первый global unicast IPv4 hostname'а, иначе IPv6, иначе
  интерфейсов; если остаётся loopback, а SDK регистрирует инстанс, старт
  пишет предупреждение: такой адрес недостижим с других хостов.
- **Манифест** — `backplane/services/<name>/manifests/<version>`,
  `backplane.Manifest` proto binary до 512 KB, по ключу на версию сервиса. Пишет SDK при старте:
  отсутствующий ключ создаётся, совпадающий не трогается. Другой манифест
  под той же выпущенной версией SDK **не перезаписывает**: ключ остаётся
  прежним, в лог — ошибка (версия называет один набор объявлений — выпустите
  новую), присутствие в Consul продолжается. Версию с build-метаданными
  (`+…`, dev-сборки) SDK перезаписывает. Версия без штампа (`0.0.0`) записывается как
  `0.0.0+<12 hex хэша манифеста>`: dev-сборки с разными объявлениями не
  перетирают манифесты друг друга. Отсутствующий ключ создаётся CAS'ом:
  два инстанса одной версии, стартующие одновременно, не спорят.
  Содержимое: `service`, `version`, `sdk_version`; `config` (схема, ключи
  верхнего уровня, пути Live-полей); `routes`; `internal_services`;
  `events`, `hooks`, `activities`; `subscriptions` (реакторы: событие и
  consumer); `workflows` и `schedules` (§9); `ui`; `nodes` — дерево автора
  в порядке старта (путь, вид, `optional`); `descriptors` — один
  `FileDescriptorSet` на все сервисы managed-роутов и внутреннего API.
  Манифест больше 512 KB — ошибка `Run`. **[backplane]** Консоль и
  backplane используют манифест **старшей живой версии** (версии живых
  инстансов — из их состояния); ключи версий, у которых не осталось
  инстансов, backplane удаляет. Rolling upgrade и откат не моргают.
- **Состояние инстанса** — `backplane/services/<name>/instances/<id>`,
  `backplane.InstanceState`:
  - `id`, `service`, `version`, `address`, `platform_port`, `started_at`,
    `sdk_version`, `commit` (`build.Commit`, иначе VCS-ревизия сборки);
  - `phase`: `starting` — дерево автора стартует, зависимости
    предоставляются; `serving` — инстанс принимает трафик и (если SDK
    регистрирует) стоит в каталоге; `stopping` — остановка началась,
    регистрации в каталоге уже нет;
  - `config` — эффективная конфигурация (JSON, секреты маскированы),
    `sources` — источник каждого пути (default / файл / env / KV);
  - `config_revision` — `_revision`, прочитанная **тем же запросом**, что и
    применённые значения (0 — ключа нет); `config_error` и
    `config_rejected_revision` — почему и какая ревизия не применена (§5.5);
    пусто и 0, когда последнее обновление применено;
  - `nodes` — зависимости дерева автора: путь, готова ли, ошибка;
  - `transports` — `consul`, `nats`, `temporal`, `otlp`: подключён ли,
    ошибка.

  Состояние пишется с узла `consul` **до** старта дерева автора — инстанс,
  чьи зависимости ещё ретраятся, виден с фазой `starting`; дальше — при
  каждом изменении конфигурации и фазы, а `nodes` и `transports`
  сверяются на каждом продлении сессии (TTL/3). Ключ привязан к
  Consul-сессии инстанса с TTL `BACKPLANE_CONSUL_SESSION_TTL` (30s;
  значение вне допустимых Consul 10s..24h приводится к границе) —
  умирает вместе с ним; сессия продлевается каждые TTL/3, продления,
  неудачные дольше TTL/2, пересоздают присутствие. Сессия называется
  `<instance>#<инкарнация>` (случайная на процесс): первое установление
  присутствия процесса перехватывает ключ у сессии прошлой инкарнации
  того же инстанса (упавший предшественник); сессия другой живой
  инкарнации позже — это дубликат id инстанса: ошибка в лог
  (`duplicate instance id`) и повтор с backoff. **[backplane]** Отсюда
  консоль берёт «что реально применено», backplane — базу для валидации
  override, консольный прокси — адрес и порт для relay.

**[backplane]** backplane держит blocking queries на каталог и на префикс
`backplane/services/`. `Meta` регистрации (512 символов на значение) SDK
не заполняет — всё наше лежит в KV, и внешняя регистрация деплоем ничего
про нас знать не должна.

### 4.3 Блок SDK в конфигурации

Блок `config.Backplane`, встроенный в конфигурацию автора (тег
`json:"backplane"`; `backplane.Open` требует его — `config.Backplaner`).
Env блока фиксирован для всех сервисов: `BACKPLANE_<PATH>`, путь —
JSON-имена полей блока в верхнем регистре через `_`; остальная
конфигурация читается из `<SERVICE>_*` (`HELLO_GREETER_SUFFIX`, §3.0). В
файле конфигурации блок — секция `backplane:`. Таблица — все поля блока
с умолчаниями:

| | |
|---|---|
| `BACKPLANE_CONSUL_ADDR`, `_CONSUL_TOKEN` | реестр и KV; пусто — без Consul |
| `BACKPLANE_CONSUL_DATACENTER` | датацентр запросов; пусто — датацентр агента |
| `BACKPLANE_CONSUL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | HTTPS к Consul: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат, только парой; непарсящийся PEM — ошибка `Open`. Включённый TLS блока целиком заменяет TLS из `CONSUL_*` |
| `BACKPLANE_CONSUL_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат Consul — только для разработки |
| `CONSUL_HTTP_TOKEN`, `CONSUL_HTTP_TOKEN_FILE`, `CONSUL_HTTP_SSL`, `CONSUL_CACERT`, `CONSUL_CAPATH`, `CONSUL_CLIENT_CERT`, `CONSUL_CLIENT_KEY`, `CONSUL_TLS_SERVER_NAME`, `CONSUL_HTTP_SSL_VERIFY` | стандартные переменные клиента Consul: заполняют то, что блок оставил пустым (токен, TLS при выключенном TLS блока); Consul включает только `BACKPLANE_CONSUL_ADDR` |
| `BACKPLANE_CONSUL_REGISTER` | регистрация в каталоге (`true`); `false` — регистрирует деплой |
| `BACKPLANE_CONSUL_TAGS` | теги регистрации в каталоге, JSON-список (`["blue","canary"]`) |
| `BACKPLANE_CONSUL_CHECK_INTERVAL`, `_CHECK_TIMEOUT`, `_DEREGISTER_AFTER` | health check регистрации: интервал (`10s`), таймаут (`5s`), снятие из каталога после стольких в critical (`1m`) |
| `BACKPLANE_CONSUL_SESSION_TTL` | TTL сессии, под которой живёт состояние инстанса (`30s`; вне 10s..24h приводится к границе): продление каждые TTL/3, после TTL/2 неудачных продлений присутствие пересоздаётся; после смерти процесса состояние исчезает не позже чем через TTL |
| `BACKPLANE_INSTANCE`, `BACKPLANE_ADVERTISE` | id инстанса (по умолчанию `<service>-<hostname>`) и адрес (по умолчанию `POD_IP`, иначе адрес hostname'а, §4.2); опции `backplane.Instance` / `Advertise` — выше них |
| `BACKPLANE_INTERNAL_PORT` | платформенный порт (`9400`, 1..65535) |
| `BACKPLANE_INTERNAL_SECRET` | секрет платформенного порта (§11.1); пусто — проверка выключена, `Run` пишет предупреждение |
| `BACKPLANE_INTERNAL_SECRET_PREVIOUS` | прежний секрет, принимаемый наравне с текущим, пока идёт ротация (§13); без `_INTERNAL_SECRET` не действует |
| `BACKPLANE_PUBLIC_PORT` | публичный порт managed-роутов (`8080`, 1..65535) |
| `BACKPLANE_NATS_URL`, `_NATS_CREDS` | события; пусто — без NATS; `_NATS_CREDS` — содержимое `.creds`-файла |
| `BACKPLANE_NATS_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | TLS к NATS: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат, только парой; непарсящийся PEM — ошибка старта узла `nats` |
| `BACKPLANE_NATS_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат NATS — только для разработки |
| `BACKPLANE_NATS_PUBLISH_TIMEOUT` | предел `Publish`, чей `ctx` без дедлайна (`5s`) |
| `BACKPLANE_NATS_MAX_AGE`, `_NATS_MAX_BYTES` | хранение собственного стрима событий `bp_<service>`: возраст (`168h`) и размер в байтах (`0`); `0` — без ограничения (§8) |
| `BACKPLANE_NATS_REPLICAS` | реплики стрима событий и стрима dead letters сервиса (`1`, от 1 до 5) |
| `BACKPLANE_NATS_DEDUP_WINDOW` | окно дедупликации стрима событий (`2m`); `0 < окно ≤ max_age`, иначе ошибка `Open` |
| `BACKPLANE_NATS_DLQ_MAX_AGE` | хранение dead letters `bp_dlq_<service>` (`720h`); `0` — без ограничения |
| `BACKPLANE_TEMPORAL_ADDR`, `_TEMPORAL_NS` | хуки, активити, workflows; пустой адрес — без Temporal; namespace (`default`; поле `ns`: `namespace` — зарезервированное слово CEL) |
| `BACKPLANE_TEMPORAL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | TLS к Temporal: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат (mTLS), только парой; непарсящийся PEM — ошибка старта узла `temporal` |
| `BACKPLANE_TEMPORAL_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат сервера — только для разработки |
| `BACKPLANE_TEMPORAL_API_KEY` | API key Temporal Cloud (`Authorization: Bearer`); включает TLS и без `_TLS_ENABLED` |
| `BACKPLANE_TEMPORAL_DIAL_TIMEOUT` | одна попытка подключения (`2s`): первая — на старте узла `temporal`, следующие — в фоне; он же таймаут проверки соединения |
| `BACKPLANE_TEMPORAL_HOOK_TIMEOUT` | дедлайн вызова хука, если его не задали ни `ctx`, ни вызов, ни объявление (`30s`, §3.4) |
| `BACKPLANE_TEMPORAL_WORKER_ENABLED` | `false` — реплика без worker'а активити и workflows; worker вызовов хуков работает и здесь (§9) |
| `BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITIES`, `_MAX_CONCURRENT_WORKFLOW_TASKS` | параллелизм worker'а (`0` — умолчание Temporal; отрицательное — ошибка `Open`) |
| `BACKPLANE_TEMPORAL_WORKER_ACTIVITY_POLLERS`, `_WORKFLOW_POLLERS` | поллеры worker'а (`0` — умолчание Temporal; отрицательное — ошибка `Open`) |
| `BACKPLANE_ENVIRONMENT` | `deployment.environment.name` телеметрии и `environment` в `/_backplane/info`; пусто — атрибута нет |
| `BACKPLANE_LOG_LEVEL` | уровень лога (`info`); Live — меняется из консоли на лету, непарсящееся значение оставляет прежний уровень с предупреждением в лог. С `backplane.Logger(l)` не применяется: уровень, выводы и OTLP-tee переданного логгера — дело автора |
| `BACKPLANE_SHUTDOWN_TIMEOUT` | бюджет всей остановки (`25s`); `terminationGracePeriodSeconds` — больше него |
| `BACKPLANE_SHUTDOWN_DRAIN` | пауза между дерегистрацией и закрытием listener'ов, чтобы балансировщики заметили уход (`3s`); `0 ≤ drain < timeout` |
| `BACKPLANE_SHUTDOWN_LISTENERS` | сколько публичные listener'ы и внутреннее API дорабатывают запросы в полёте (`10s`, больше 0); потом стримы обрываются, а контексты внутренних вызовов отменяются |
| `BACKPLANE_SHUTDOWN_RESERVE` | запас бюджета на дерево автора, зависимости и телеметрию после listener'ов (`5s`, не меньше 0); `drain + listeners + reserve ≤ timeout`, иначе ошибка `Open` — короткий `timeout` требует коротких этапов |
| `BACKPLANE_HEALTH_INTERVAL`, `_HEALTH_TIMEOUT` | пробы (readiness, liveness, startup) пересчитываются раз в интервал (`5s`), одна проверка — не дольше таймаута (`3s`); `0 < timeout ≤ interval`, иначе ошибка `Open`. Тем же интервалом `deps.ProbeOptional` проверяет опциональные зависимости |
| `BACKPLANE_SERVER_READ_HEADER_TIMEOUT` | HTTP: чтение заголовков запроса (`10s`); он же — сколько новое соединение может молчать, пока cmux определяет протокол |
| `BACKPLANE_SERVER_IDLE_TIMEOUT` | HTTP: простой keep-alive соединения (`2m`) |
| `BACKPLANE_SERVER_MAX_HEADER_BYTES` | HTTP: предел заголовков запроса (`1048576`); больше — `431` |
| `BACKPLANE_SERVER_GRPC_MAX_RECV_MSG_SIZE` | gRPC: предел входящего сообщения (`4194304`) |
| `BACKPLANE_SERVER_GRPC_KEEPALIVE_MIN_TIME`, `_GRPC_PERMIT_WITHOUT_STREAM` | gRPC: минимальный интервал keepalive-пингов клиента (`30s`; Envoy пингует чаще умолчания grpc-go 5m) и пинги на соединении без стримов (`true`) |
| `BACKPLANE_PPROF` | `/debug/pprof/*` на платформенном порту за секретом (`false`) |
| `BACKPLANE_CONFIG_FILE` | файл конфигурации (YAML по `.yaml`/`.yml`, иначе JSON) той же формы, что структура; слой ниже env. Не поле блока: читается до загрузки |

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
    Cache   store.Config   `json:"cache"`
}

type State struct {
    Store   deps.Dependency[*store.DB]
    Cache   deps.Optional[*store.DB]
    Greeter *greeter.Greeter
}

func NewState(root backplane.Root[Config]) (*State, error) {
    cfg := root.Config() // копия; Live-поля общие с живой конфигурацией
    st := &State{
        Store: deps.NewDependency(root, store.New(&cfg.Store)),
        Cache: deps.NewOptional(root, store.New(&cfg.Cache), deps.Name("cache")),
    }
    g, err := greeter.New(root, &cfg.Greeter, st.Store) // всё — аргументами
    ...
}

svc, err := backplane.Open(ctx, NewState) // конфиг → Root → NewState
svc.GRPC(svc.State().Greeter.Register, route.Transcode())
err = svc.Run(ctx)
```

**Open и Run.** `backplane.Open(ctx, newState, opts...)` загружает
конфигурацию, проверяет блок SDK, строит State конструктором и возвращает
`*backplane.Service[State]`; ничего не слушает. `ctx` ограничивает только
загрузку (обязательное Live-поле может ждать Consul, §5.3) — дальше
сервис от него не зависит. Невалидная конфигурация и ошибка конструктора —
ошибка `Open`, взятое к этому моменту освобождается. Между `Open` и `Run`
автор объявляет роуты, внутреннее API, UI и пробы на `Service`. `Run(ctx)`
запечатывает объявления, поднимает сервис и блокируется до конца `ctx`,
SIGINT/SIGTERM или ошибки горутины узла; затем останавливает всё.
Объявление после `Run` — ошибка программиста, паника. Второй `Run`, как и
`Run` после `Close`, возвращает `backplane.ErrClosed`. `svc.Close()`
освобождает то, что взял `Open` (горутины живой конфигурации), если `Run`
вызываться не будет; повторный `Close` безопасен, `Run` закрывает сам.

Опции `Open`: `backplane.Name(n)`, `Version(v)`, `Instance(id)`,
`Advertise(addr)`, `Logger(l)`, `ConfigOptions(opts...)` (в `config.Open`),
`KeepSlog()`, `GRPCServerOptions(opts...)` (§3.1), `RequireNATS()` и
`RequireTemporal()` — соединение входит в readiness: пока оно не поднято,
инстанс не ready; без настроенного транспорта — ошибка `Open`. Имя
сервиса — `Name`, иначе `build.Service` из ldflags, иначе последний
элемент пути main-пакета (`.../cmd/hello` → `hello`), так что `go run`
работает; без имени или с именем не по `^[a-z][a-z0-9-]*$` (§17) — ошибка
`Open` (`backplane.ErrConfig`). Версия — `Version`,
иначе `build.Version`, иначе версия модуля, иначе `0.0.0+<commit>`, иначе
`0.0.0` (в манифесте — с хэшем, §4.2). По умолчанию `Run` направляет
`log/slog` в логгер сервиса (через него логируют OTel SDK и другие
библиотеки) и восстанавливает прежний default при выходе; `KeepSlog`
оставляет `slog` в покое.

`Service` даёт `State()`, `Name()`, `Identity()` (сервис, версия,
инстанс, адрес, окружение), `Log()` и объявления: `HTTP`, `GRPC`,
`GraphQL`, `Route` (§3.1), `Internal` (§3.2), `UI` (§3.6),
`LivenessProbe`/`ReadinessProbe`/`StartupProbe` (§4.1); ws-proto —
`wsproto.Serve(svc, ...)`.

**Дерево узлов** (`deps`). `Root` — корень; под ним автор создаёт узлы и
передаёт их друг другу аргументами конструкторов. `Root` — сам узел
(`deps.Component`), плюс `Config()` — копия загруженной конфигурации
(обычные поля не меняются, Live-поля общие для всех копий и обновляются на
месте) и `Identity()`. Встраивать `Root` в State не обязательно.

- `deps.Component` + `deps.NewComponent(parent, name)` — код автора;
  встраивается в его тип. Нулевой `Component` никуда не логирует, а
  `Go`/`OnStart`/`OnStop` на нём — паника;
- `deps.Dependency[T]` + `deps.NewDependency(parent, provider, opts...)` —
  обязательный внешний ресурс: его старт ретраится с backoff до успеха
  (сервис жив, но не ready), он входит в readiness своей пробой,
  закрывается на стопе. Трафик — публичный и внутренний — идёт только когда
  готовы все обязательные зависимости, и прекращается до их закрытия, так
  что `Get()` в handler'ах всегда даёт значение; `Ready()`, `Err()`;
- `deps.Optional[T]` + `deps.NewOptional(parent, provider, opts...)` —
  зависимость, без которой сервис работает: старт её не ждёт, provide
  ретраится в фоне до успеха или остановки, в readiness не входит.
  `Get()` возвращает `(T, bool)` — есть ли она сейчас; `Err()` — почему нет.
  С `deps.ProbeOptional()` предоставленная зависимость проверяется своей
  пробой раз в `Health.Interval`: пока проба падает, `Get()` отвечает
  `(zero, false)`, а `Err()` — ошибкой пробы; значение не закрывается и
  возвращается, когда проба проходит;
- `deps.Singleton[T]` + `deps.NewSingleton(parent, factory)` — ленивое
  значение: строится при первом `Get(ctx)` (неудача — повтор при
  следующем), закрывается на стопе, после стопа — `deps.ErrClosed`. В
  readiness не входит; фабрика не создаёт дочерних узлов. `Get` держит
  замок синглтона, пока работает фабрика: параллельные `Get` ждут эту
  одну сборку и получают её значение (при неудаче — пробуют сами по
  очереди), свой `ctx` во время ожидания они не смотрят; стоп ждёт
  идущую сборку и закрывает её результат. Медленную фабрику ограничивает
  `ctx`, который она получает.

Опции зависимостей: `deps.Name(n)` (имя узла вместо имени провайдера —
две базы одного вида), `deps.Backoff(min, max)` (пауза между попытками,
по умолчанию 1s..30s с джиттером), `deps.ProvideTimeout(d)` (одна попытка
`Provide`, 30s; зависшая попытка ретраится как неудачная; `ctx` попытки
заканчивается вместе с ней — провайдер его не хранит),
`deps.ProbeTimeout(d)` (одна проба; по умолчанию — `Health.Timeout` у
обязательной, `Health.Interval` у `ProbeOptional`),
`deps.ProbeOptional()` (только `NewOptional`). Метрики:
`backplane.dependency.provide.attempts{node, outcome}` на каждую попытку и
`backplane.dependency.ready{node}` — 1, пока значение предоставлено (и
проходит пробу при `ProbeOptional`). Для тестов и значений, которыми
сервис владеет сам, — `deps.Static(v)` и `deps.StaticOptional(v)`;
нулевой `Optional` — отсутствующая зависимость. `IsZero()` у каждого вида
отличает несозданный узел: `Get()` нулевой `Dependency` — нулевое
значение без паники, `Err()` нулевой зависимости — `deps.ErrNotReady`.

Провайдер (`deps.Provider[T]`: `Name`, `Provide`, `Probe`, `Close`) — форма
будущих contrib-модулей (`contrib/pgx`, `contrib/valkey`, `contrib/s3`):
секция конфигурации + `New(*Config) deps.Provider[T]`. `Provide` получает
`deps.Scope` своего узла; у обязательной зависимости он может создавать под
ним дочерние узлы (они стартуют вместе с ней). Для
разового случая — `deps.Func(fn, deps.WithProbe(...), deps.WithClose(...))`,
имя узла — от типа (`*pgxpool.Pool` → `pool`). Фабрика синглтона
(`deps.Factory[T]`) — провайдер без пробы.

Каждый узел — `deps.Scope`: `Name`, свой путь `Path` (`greeter/templates`),
логгер `Log` с `node=<путь>`, `Tracer` и `Meter` (`<service>/<путь>`),
`Span(ctx, name, fn)` (и `deps.SpanValue` для fn со значением), `Go`,
`OnStart`/`OnStop`. Одинаковые имена получают суффикс (`store-2`). Узлы
создаются до `Run` (после — паника; исключение — `Provide` обязательной
зависимости); дерево попадает в манифест (`nodes`).

**Lifecycle.** Всё — и части SDK, и дерево автора — одно дерево узлов.
Старт — в глубину в порядке создания; остановка — в точно обратном
порядке, поэтому узел всегда останавливается раньше всего, что создано до
него: компонент — раньше зависимости, которую ему передали, ребёнок —
раньше родителя. Узлы сервиса:

`config → telemetry → health → platform → consul → nats → temporal → hooks → <дерево автора> → internal-api → reactors → worker → schedules → public:<addr> → drain → register → serving`

Не все узлы есть всегда: `nats` — когда задан `nats.url`; `temporal` и
`hooks` — когда задан `temporal.addr` (`hooks` — worker вызовов хуков вне
workflow; ничего не запускает, если хуков нет); `reactors` — когда
объявлены реакторы и есть NATS; `worker` — когда объявлены активити или
workflows, есть Temporal и `temporal.worker.enabled`; `schedules` — когда
есть Temporal (сверка идёт и без объявлений, §9); `register` — когда
задан Consul; `public:<addr>` — по одному на публичный адрес. Без
транспорта объявленное остаётся в манифесте, `Run` пишет предупреждение.
Соединения стоят до дерева автора, поэтому компонент может публиковать
события и вызывать хуки уже из `OnStart` и ещё из `OnStop`; `reactors`,
`worker` (активити и workflows автора) и `schedules` идут после дерева и
останавливаются раньше него.

`consul` публикует манифест и состояние инстанса (фаза `starting`) до
дерева автора и снимает состояние последним; `register` — регистрация в
каталоге и фаза `serving`, на остановке — уход из каталога и фаза
`stopping` (§4.2).

Остановка узла: отмена его контекста, stop-хуки в обратном порядке,
ожидание его горутин. Узел, чей старт упал, тоже останавливается — stop-
хуки терпят частичный старт. Горутины — `scope.Go(fn)`: запускаются после
старта узла, на стопе отменяются и дожидаются; ошибка или паника любой
останавливает сервис.

Остановка — в пределах одного бюджета `Shutdown.Timeout`: `serving`
снимает readiness (`NOT_SERVING`) → `register` снимает регистрацию в
каталоге и пишет фазу `stopping` → `drain` ждёт `Shutdown.Drain`, пока
балансировщики заметят уход → публичные listener'ы перестают принимать и
дорабатывают запросы в полёте → `schedules`, `worker`, `reactors` →
`internal-api` закрывает приём и ждёт внутренних вызовов в полёте →
дерево автора (компоненты раньше своих зависимостей) → `hooks`,
`temporal`, `nats` → `consul` удаляет состояние инстанса (сессию) →
платформенный порт → health → telemetry → config. Этапы делят бюджет:
публичные listener'ы и `internal-api` вместе получают одно окно
`Shutdown.Listeners` — по его концу открытые стримы обрываются, а
контексты внутренних вызовов в полёте отменяются; окно и `drain`
укорачиваются так, чтобы дереву автора, зависимостям и телеметрии
осталось не меньше `Shutdown.Reserve`. У `worker` и `reactors` своего
окна нет: они ждут активити и обработчиков в полёте до конца общего
бюджета, поэтому обработчик, который не заканчивается,
съедает и `Reserve` (§8, «Остановка реакторов»). Узел, не уложившийся в
бюджет, — ошибка остановки: `Run` возвращает её, остальные узлы всё
равно останавливаются. Повторный SIGINT/SIGTERM во время
остановки (или сигнал, пришедший, когда остановка уже идёт по другой
причине) — немедленный выход процесса с кодом 1 после записи в лог. Сигнал во время старта —
чистая остановка уже поднятого, `Run` возвращает `nil`; обязательная
зависимость, которая ещё ретраится, при этом бросается, а уже
предоставленные закрываются. Health один: пробы регистрируются по виду;
`grpc.health.v1`, HTTP-пробы и Consul-check читают одно и то же.

Серверы инструментированы: gRPC обоих классов портов — otelgrpc (без
вызовов `grpc.health.v1`), HTTP публичных портов — otelhttp, ws-proto —
span на вызов; HTTP платформенного порта (пробы, бандл, info, pprof)
span'ов не пишет. Логи несут trace_id; паника handler'а на любом сервере
— ошибка запроса, не процесса (§3.1). Лимиты серверов — `config.Server`
(§4.3) на обоих классах портов.

**Consul presence.** Сессия с `LockDelay` 1 мс (дефолтные 15 с не дали бы
перезапущенному инстансу опубликовать состояние); ключ, удерживаемый
прошлой инкарнацией того же инстанса, перехватывается при первом
установлении присутствия процесса (§4.2). Consul ни на старте, ни потом
не роняет сервис: присутствие восстанавливается в фоне (§4.5).

**Логирование.** Логгер по умолчанию — JSON в stdout плюс OTLP-tee; его
уровень — Live-поле `log_level`, изменение применяется на лету, неверное
значение оставляет прежний уровень (предупреждение в лог). Логгер,
переданный `backplane.Logger(l)`, принадлежит автору: `log_level` к нему
не применяется, OTLP-tee SDK не добавляет. Конфигурация пишет в лог сервиса
каждый отказ в применении обновления (warn, с ошибкой и ревизией) и каждый
переход Consul-слоя: недоступен (warn) и восстановлен (info); метрики
`backplane.config.updates{outcome=applied|rejected}` и
`backplane.config.degraded`.

### 4.5 Старт сервиса

`Open`: загрузить конфигурацию (файл, env; Consul KV для Live-полей, если
Consul задан и доступен — иначе предупреждение и повтор в фоне) →
проверить блок SDK → собрать State конструктором автора. `Run`: собрать и
проверить манифест (ошибки объявлений — ошибка `Run` до того, как что-то
слушает) → телеметрия → health `NOT_SERVING` → платформенный порт →
манифест и состояние в Consul (фаза `starting`, если Consul есть; в
фоне) → соединения NATS и Temporal (одна короткая попытка, дальше в фоне)
и worker хуков → дерево узлов автора (обязательные зависимости ждут
готовности) → внутреннее API открывается → реакторы, worker, сверка
расписаний (в фоне, как только есть соединение) → порты внешнего API →
регистрация в каталоге Consul, фаза `serving` → health `SERVING`. Без Consul, NATS и Temporal сервис работает в
объёме внешнего API и конфигурации из env/файла.

**Деградация.** Ни одна из внешних систем не роняет сервис — ни на старте,
ни потом; readiness от них не зависит, кроме NATS и Temporal с
`backplane.RequireNATS()` / `RequireTemporal()`:

| система · что | недоступна на старте | пропала во время работы | вернулась |
|---|---|---|---|
| Consul · конфигурация | Consul-слой пуст, Live-поля из env/файла/defaults, повтор с backoff 1s..30s (`config.ConsulBackoff`); исключение — обязательное Live-поле, которое есть только в KV: держит `Open` до ответа Consul или конца ctx (§5.3) | последние значения KV остаются в силе; переход пишется в лог (warn) и в `backplane.config.degraded` | слой перечитывается, изменения применяются по обычным правилам (§5.5); переход — в лог (info) |
| Consul · присутствие | манифест, состояние и регистрация ретраятся в фоне (backoff 2s..1m); инстанс работает, но не виден | продление сессии ретраится TTL/2, затем присутствие пересоздаётся с backoff; состояние исчезает, когда истекает сессия; регистрация в каталоге живёт в агенте | новая сессия, состояние переписывается, регистрация подтверждается (если инстанс уже `serving`) |
| NATS · `Publish` | клиент переподключается бесконечно (2 s + jitter до 1 s); `Publish` сразу — `event.ErrUnavailable`; стрим `bp_<service>` создаётся в фоне после подключения (backoff 0.5s..15s) | сразу `ErrUnavailable` — без буферизации | снова публикует |
| NATS · реакторы | стримы и consumers создаются в фоне после подключения (backoff 0.5s..15s) | потребление стоит; неподтверждённое будет доставлено заново; consumer, удалённый на сервере, пересоздаётся с позиции реактора (старейшее неподтверждённое, иначе после последнего увиденного) | потребление продолжается с позиции durable consumer'а |
| Temporal · `Call` (хуки) | одна попытка подключения (`dial_timeout`), дальше в фоне (backoff 1s..30s); `Call` сразу — `hook.ErrUnavailable` | подключённый клиент проверяет соединение каждые 5 s; вызов падает ошибкой транспорта в пределах своего дедлайна | вызовы проходят |
| Temporal · worker'ы, расписания | worker'ы стартуют и расписания сверяются, как только соединение поднимется | worker'ы SDK Temporal переподключаются сами; активити в полёте добегают или истекают по таймаутам Temporal | опрос очередей продолжается |
| OTLP · экспорт | сигнал без endpoint не экспортируется; с endpoint экспортёр ретраит и отбрасывает | батчи отбрасываются после ретраев экспортёра | экспорт продолжается |
| readiness | Consul и OTLP не входят никогда; NATS и Temporal — только с `RequireNATS()` / `RequireTemporal()`: не ready, пока соединения нет | то же | ready снова, как только соединение есть |

Состояние каждого соединения — в `transports` состояния инстанса (§4.2) и
в метрике `backplane.transport.connected{transport}`.

### 4.6 Тесты компонентов — `backplanetest`

Компоненты тестируются без `Open`, `Run`, портов и Consul: транспорты
заменены записывающей заглушкой, дерево — то же, что в сервисе.

```go
h := backplanetest.New(t, backplanetest.Name("hello"))           // дерево сервиса ("test" по умолчанию)
cfg := backplanetest.Config[greeter.Config](t, map[string]string{"SUFFIX": "?"})
g, _ := greeter.New(h.Root(), &cfg, deps.Static(db))              // узлы — под h.Root()
f := flows.New(h.Root(), g, deps.Static(db))
backplanetest.Answer(h, f.Greet, fn)                             // ответ на хук: Call и WorkflowCall
h.Start()                                                        // provide, горутины; стоп — в t.Cleanup
backplanetest.Ready(h)                                           // readiness дерева, как у пробы
backplanetest.Events(h, g.Greeted())                             // что опубликовано в event.Ref
backplanetest.Activity[flows.EchoIn, flows.EchoOut](ctx, h, "Echo", in) // вызов активити
backplanetest.ReactConsumer(ctx, h, audit.Consumer, v)           // доставка реактору
backplanetest.SetLive(&cfg.Suffix, "!")                          // Live как из консоли; Watch срабатывает
w := backplanetest.Workflows(h)                                  // testsuite Temporal с worker'ом сервиса
h.Manifest()                                                     // что объявлено
```

- **Harness.** `New(t, opts...)`: `Name(service)` — имя сервиса (события
  публикуются как `<name>.<Event>`, реактор на своё событие —
  `<name>.<Event>`), `StopBudget(d)` — бюджет остановки в `t.Cleanup`
  (10 s; не уложился — тест падает). `h.Root()`, `h.Start()`,
  `h.Service()`, `h.Manifest()` (ошибка объявлений — тест падает).
  `Ready(h)` — `nil` или ошибка `ErrNotReady` с каждой неготовой
  обязательной зависимостью и причиной.
- **Конфигурация.** `Config[C](t, vars)` / `LoadConfig[C](ctx, vars)` —
  секция или вся конфигурация, как её загрузит сервис: умолчания схемы и
  `vars` (имена как в env сервиса без префикса: `{"SUFFIX": "?"}` для
  секции, `{"GREETER_SUFFIX": "?"}` для всей); незнакомое имя — ошибка;
  все `Validate` вызываются. Файл, env процесса и Consul не читаются.
  `SetLive(&live, v)` — обновление Live-поля, как из консоли.
- **Хуки.** `Answer(h, ref, fn)` отвечает на `Call` и, в `Workflows`, на
  `WorkflowCall`; `CallKey(ctx)` в `fn` — `hook.Key` вызова. Хук без
  `Answer` отвечает `hook.ErrUnavailable` (`WorkflowCall` —
  `hook.ErrNoBinding`), как в продакшене без транспорта или биндинга.
- **События.** `Events(h, ref)` — значения, `EventsWithMeta(h, ref)` — с
  `Key`, `ID`, `Time`, `Headers`. `FailPublish(h, err)` — `Publish`
  падает с `err`; `Unavailable(h)` / `Available(h)` — оба транспорта
  пропадают (`event.ErrUnavailable`, `hook.ErrUnavailable`) и
  возвращаются.
- **Реакторы.** `React(ctx, h, path, event, v, opts...)` — реактору узла
  `path` (`""` — корень; реактор с `event.Consumer` находится по событию,
  если он такой один), `ReactConsumer(ctx, h, consumer, v, opts...)` — по
  имени consumer'а. Обработчик вызывается один раз с `Timeout` реактора и
  полным `event.Delivery` (новый `ID`, `Attempt` 1, `Consumer`,
  расширения `instance` и `version`); `WithKey`, `WithID`, `WithAttempt`,
  `WithHeader` меняют доставку. Результат — ошибка обработчика; повторов
  и DLQ нет.
- **Активити.** `Activity[Req, Res](ctx, h, name, in)` вызывает активити
  один раз, как шаг биндинга: `activity.InfoOf` даёт `Attempt` 1,
  уникальный `Key` и дедлайн `ctx`; `Heartbeat` ничего не делает.
- **Workflows.** `Workflows(h)` — `testsuite.TestWorkflowEnvironment` с
  регистрациями worker'а сервиса: активити `Handle` по имени через
  envelope, workflows `workflows.Declare`, `activity.Workflow`,
  `workflows.Register`; хуки — Nexus-сервис `<service>.Hooks` среды,
  отвечающий через `Answer`. Одна среда — один workflow.
  `WorkflowActivity[Req, Res](w, name, in)` запускает активити на
  workflow, как шаг биндинга.

Активити или реактор, которых нет, — `backplanetest.ErrNotDeclared`.

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
Сервер — пакет `internal/config` (компонент `config` дерева backplane).

- Источник правды override-слоя — **PostgreSQL backplane**: таблица
  `config_revision` — ревизии сервиса (номер с 1 на сервис, значения,
  автор, комментарий, время, `rollback_of`), append-only; `config_current`
  — текущая ревизия сервиса (всегда последняя). Откат — новая ревизия со
  значениями старой и `rollback_of` = её номер.
- **Override** — плоская карта «Live-путь манифеста (`config.live`,
  через точку: `greeter.suffix`) → JSON-значение». Переопределяются
  только Live-пути целиком: путь не из `config.live` (обычное поле,
  секция, путь внутри Live-поля) — отказ `NOT_LIVE`; `null` — отказ
  (чтобы снять override, путь не передают). Путь, которого нет в
  ревизии, берёт значение из слоёв инстанса (defaults, файл, env).
- Валидация **до** сохранения: override кодируется в KV (§5.3) и
  раскладывается тем же декодером, что читает SDK (xconf env-кодирование
  против схемы инстанса), затем накладывается как слой xconf (объекты
  сливаются по ключам, остальное заменяется) на эффективную конфигурацию
  **каждого живого инстанса** (`config` его состояния, §4.2) и
  проверяется схемой манифеста **версии этого инстанса** (schemapb:
  типы, границы, CEL); пути, которые в его версии не Live, для него
  пропускаются — его SDK их игнорирует. Нет живых инстансов — база —
  defaults схемы старшего манифеста. Ошибки, которые конфигурация инстанса
  даёт и без override, не сообщаются: так маскированные секреты (`***` в
  состоянии) считаются присутствующими и валидными, а схема
  проверяет только то, что ломает override. Пути, которые текущая ревизия
  переопределяла, а новая — нет, валидируются на значении из состояния
  инстанса (значения нижних слоёв backplane не видит). Без схемы —
  проверяется только JSON, Live-путь и что его первый сегмент есть в
  `config.keys`. Невалидное не сохраняется; ответ — нарушения с путём,
  инстансом (пусто — не зависит от инстанса или проверено на defaults),
  кодом (`ErrorCode` schemapb без `ERROR_CODE_`, либо `NOT_LIVE`,
  `INVALID_JSON`, `NULL_VALUE`, `UNDECLARED`) и сообщением. `Validate`
  кода автора здесь не вызывается (§5.5).
- Консоль показывает поле по каждому инстансу: эффективное значение и его
  источник (default / файл / env / KV), применённую ревизию и ошибку
  применения — из состояния инстанса.
- API — `backplane.console.v1.ConfigService` (`backplanepb/console/v1/config.proto`),
  значения — карта «Live-путь → JSON-текст»: `GetConfig` (схема, ключи и
  Live-пути старшего манифеста, текущая ревизия, по инстансу — фаза,
  эффективная конфигурация, значение и источник каждого Live-пути,
  применённая и отвергнутая ревизия, ошибка), `ListRevisions` (новые
  первыми; `before`, `page_size` — 50 по умолчанию, до 500;
  `next_before`), `ValidateOverride` (сухой прогон), `SaveRevision`,
  `Rollback` (нарушения вместо ревизии, если отвергнуто;
  `delivery_error` — ревизия сохранена, но в KV ещё не доставлена),
  `WatchConfig` (стрим: текущее состояние, затем при каждом изменении —
  снапшот реестра, ревизия этой или другой реплики). Автор ревизии —
  `console:<сессия>` из metadata `bp-console-session`, без неё — `admin`.
  Сервис зарегистрирован во внутреннем API backplane (платформенный порт);
  консоль отдаёт его по `/ws`.
- Сервис читает KV любым способом (in-process watch через SDK,
  consul-template, envconsul) и о backplane не знает.

Метрики: `backplane.config.revisions{outcome=saved|rejected}`,
`backplane.config.kv.rewrites{reason=missing|stale|edited}`,
`backplane.config.kv.failures`. Логи: сохранение (сервис, ревизия, автор,
откат), отказ (число нарушений, первое), доставка, перезапись ручной
правки (warn с ключами), начало и конец серии ошибок reconciler'а.

### 5.3 Репликация PostgreSQL → Consul

Сторона SDK — чтение `_revision` и отчёт в состоянии инстанса; остальное
— сервер. Consul может умереть или потерять данные — мы не теряем ничего:

- **Кодирование KV** — ровно то, что читает Consul-источник SDK (xconf
  `NewPrefix`, env-кодирование): ключ — `config/<service>/<Live-путь, `.`
  → `/`>`; значение Live-поля контейнерного вида схемы (object, ref, map,
  list, one-of, json) — его JSON, скалярного — текст: строка как есть,
  bool — `true`/`false`, число — десятичное (целое поле — без дроби и
  экспоненты: `1e3` → `1000`), duration — текст Go (`1m30s`; число —
  наносекунды). Без схемы решает форма значения: объект и список — JSON,
  остальное — текст. Ревизия хранит и сохранённые значения, и
  закодированные строки, так что доставка от реестра не зависит.
- **Сохранение** = serializable-транзакция в PostgreSQL (следующий номер,
  строка ревизии, `config_current`; параллельные сохранения сервиса
  повторяются), затем доставка в `config/<service>/` (до 10 s; ошибка
  доставки ревизию не откатывает — её допишет reconciler).
- **Доставка** читает префикс, затем текущую ревизию из PostgreSQL и
  пишет разницу: изменённые значения, удаление ключей, которых нет в
  ревизии, и последним — `config/<service>/_revision = <n>`. Последняя
  транзакция начинается с CAS на `_revision` (`check-index` прочитанного,
  `check-not-exists`, если его не было): реплика со старой ревизией не
  перетирает новую, проигравший CAS перечитывает и повторяет (до 3 раз).
  До 62 изменений — одна атомарная `txn`; больше — транзакции по 64
  (лимит Consul): сначала значения, затем удаления, `_revision` — в
  последней; пока она не прошла, читатель может увидеть часть новых
  значений под старой `_revision`. Папки Consul UI (ключ на `/` без
  значения) — не значения: не сравниваются и не удаляются.
- SDK читает `_revision` тем же запросом к префиксу, что и значения (сам
  ключ в конфигурацию не попадает), и публикует в состоянии инстанса
  (`config_revision`) ровно ревизию применённых значений; отвергнутая — в
  `config_rejected_revision` (§5.5).
- **Reconciler** — горутина компонента `config`: проход при старте, раз в
  `BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL` (30s) и по каждому изменению
  префикса `config/` (blocking query, ожидание 5m). Проход — один `List`
  `config/` и один запрос текущих ревизий всех сервисов; сервис, у которого
  `_revision`, значения или набор ключей отличаются от ревизии, проходит
  доставку. Причина в логе и метрике: `missing` (пусто — Consul потерял
  данные), `stale` (другая `_revision`), `edited` (ревизия совпадает,
  значения нет — ручная правка в Consul UI перетирается, warn с ключами).
  Сервисы без ревизий в PostgreSQL backplane не трогает: `config/<service>/`
  становится управляемым с первой сохранённой ревизии. Реплики работают
  одновременно: все пишут то, что в PostgreSQL, через CAS, так что
  проходы сходятся; сохранение на одной реплике другие видят по изменению
  `config/` (и шлют в свои `WatchConfig`).
- Пока Consul недоступен, сервисы работают на последних значениях или на
  env/файле; backplane копит ревизии и доставляет, когда Consul вернётся.
  Исключение — обязательное Live-поле без default, которое есть только в
  KV: оно держит `Open`, пока Consul не ответит или не кончится ctx
  `Open` — тогда `Open` возвращает ошибку. ctx `Open` ограничивает только
  загрузку: запущенный сервис от него не зависит. Ошибка в обычном поле
  при этом не ждёт Consul — `Open` падает сразу.
- Необязательное Live-поле без default, которого нет ни в одном слое,
  читается нулевым значением и получает значение из KV, как только оно
  появится; все копии секции видят его (ячейки Live-полей создаются при
  `Open`).

### 5.4 GitOps [backplane]

Обычные поля остаются в git → Argo → env; их изменение — rollout, и это
правильно. Live-значения живут вне git — это и есть смысл динамической
конфигурации (AWS AppConfig, OpenFeature). Argo не управляет объектами, в
которые пишет backplane, поэтому self-heal их не откатывает. «UI → коммит
в git → Argo → ConfigMap → SDK следит» возможен позже как второй канал.

### 5.5 Проверка значений и отказ в применении

Схема (schemapb: типы, границы, CEL) — первая проверка. Вторая — код
автора: если конфигурация или любая её секция (поле-структура, указатель,
элемент списка или map) реализует `config.Validator`
(`Validate() error`, приёмник значение или указатель), SDK вызывает её —
сначала секции в глубину, затем объемлющую структуру; ошибка секции
предваряется её путём (`limits: max must be positive`). `Validate`
встроенной структуры, продвинутый в объемлющую, вызывается один раз.

- **`Open`**: ошибка схемы или `Validate` — ошибка `Open`
  (`config: validate: …`); сервис не стартует — в том числе если
  невалидное значение пришло из KV.
- **Обновление из KV**: значения сначала раскладываются в новую копию
  конфигурации и проверяются схемой и `Validate`. Прошли — Live-поля
  обновляются на месте, `Watch` срабатывают, `config_revision` = ревизия
  этих значений, `config_error` пуст, `config_rejected_revision` = 0. Не
  прошли — обновление **не применяется целиком**: Live-поля и
  эффективная конфигурация в состоянии инстанса остаются прежними,
  `config_revision` — прежним, `config_error` — причина,
  `config_rejected_revision` — ревизия отвергнутого обновления; в лог —
  warn с ошибкой и ревизией, метрика
  `backplane.config.updates{outcome=rejected}` (повтор того же отказа той же
  ревизии не дублируется). Следующее прошедшее обновление снимает отказ.

backplane проверяет override до сохранения только схемой (§5.2): `Validate`
живёт в коде сервиса, и ревизию, которую схема пропустила, а `Validate`
отверг, консоль видит по `config_rejected_revision` и `config_error` каждого
инстанса.

## 6. Gateway — Envoy [backplane]

Envoy — единственный вход. backplane — его control-plane по **xDS**
(`internal/xds`, `envoyproxy/go-control-plane`): ADS — один gRPC
bidi-стрим на все ресурсы; сервер отвечает и в Delta-режиме (им ходит
bootstrap), и в state-of-the-world. ADS слушает свой адрес
`BACKPLANE_XDS_LISTEN` (`:18000`) — узел дерева backplane (адрес
открывается при старте, занятый адрес — ошибка старта), а не публичный
порт SDK: публичный роут попал бы в манифест backplane и в маршруты самого
Envoy.

**Снапшот.** Один на всех: все Envoy — одна группа узлов, node id и
cluster из bootstrap только пишутся в лог. Снапшот строится из `Catalog`
registry (`xds.Build` — чистая функция каталога) после каждого изменения,
с debounce 200 мс; одинаковое содержимое (хэш ресурсов) не отдаётся
повторно, версия снапшота — счётчик. Пока registry не синхронизирован,
снапшота нет — Envoy после рестарта backplane держит прежний конфиг, а не
пустой; readiness backplane ждёт первого снапшота. От сервиса берутся
роуты его `Latest()` манифеста, трафик идёт на `Healthy()` инстансы.

- **LDS** — один listener `public` на `0.0.0.0:BACKPLANE_XDS_HTTP_PORT`
  (10000, как в compose): HTTP connection manager (HTTP/1.1 и h2c,
  `use_remote_address`, порт в Host отбрасывается перед выбором virtual
  host), RDS `public` по ADS. HTTP-фильтры по порядку: `cors`,
  `grpc_web`, `grpc_json_transcoder`, `buffer`, `router`. `cors` и
  `router` работают всегда; `grpc_web` — везде, кроме роутов, где он
  выключен (все, кроме Connect: Envoy не принимает пустой per-route
  конфиг, включающий выключенный фильтр, а своего конфига у `grpc_web`
  нет); `grpc_json_transcoder` и `buffer` выключены на listener'е и
  включаются конфигом роута. WebSocket upgrade выключен на listener'е и
  включается роутам ws-proto и консоли. TLS — позже (параметр конфигурации
  listener'а).
- **RDS** — таблица `public`. Роут с `host` попадает в virtual host этого
  домена (`*.example.com` — wildcard), без `host` — в `default` (`*`).
  Порядок как в Go-муксе SDK (§3.1): длиннее префикс — раньше, при равном
  префиксе — точный хост, затем самый длинный wildcard, затем роут без
  хоста; поэтому virtual host домена содержит и роуты покрывающих его
  wildcard'ов, и роуты без хоста. Совпадение хоста и префикса у двух
  сервисов — выигрывает первый по имени, второй пропускается с
  предупреждением. Матч — по `prefix` (gRPC — `/<service>/`).
  `Route.policy` → маршрут: `timeout` — `route.timeout`, `idle_timeout` —
  `route.idle_timeout`, `retry` — `retry_policy` (`num_retries` =
  `attempts − 1`, `per_try_timeout`, `retry_on` через запятую; пусто —
  `connect-failure,refused-stream`), `cors` — per-route `CorsPolicy`
  фильтра `cors` (`allow_origin_string_match` точными строками, `*` —
  любой origin), `max_request_bytes` — per-route `buffer` (тело
  буферизуется, больше лимита — 413; только HTTP и GraphQL: буфер сломал
  бы стримы gRPC и WebSocket, там действует лимит сообщений самого
  сервиса, backplane пишет предупреждение). Незаданное поле — умолчание
  Envoy (timeout 15s), кроме ws-proto: у него route timeout выключен
  (upgrade-соединение — не запрос; его ограничивает `idle_timeout`).
  По видам: ws-proto — upgrade `websocket`; Connect — gRPC-Web и
  REST-JSON: `grpc_json_transcoder` с дескрипторами роута, иначе
  `Manifest.descriptors`, `services` роута, `auto_mapping` (`POST
  /<service>/<Method>` с JSON-телом), пути `google.api.http` — если лежат
  под префиксом роута. Дескрипторы проверяются до отдачи: без сервиса
  роута транскодинга нет (остаются gRPC и gRPC-Web) и есть
  предупреждение — иначе Envoy отверг бы всю таблицу. Нативный Connect
  protocol не транскодируется.
- **Консоль** (§11.3) — роут на cluster `backplane_console`: с
  `BACKPLANE_CONSOLE_HOST` — `/` своего virtual host (на этом хосте только
  консоль), с `BACKPLANE_CONSOLE_PREFIX` — path-separated prefix
  (`/backplane` и `/backplane/...`) в `default`, без обоих — `/` в
  `default` после всех роутов сервисов. Путь не переписывается: консоль
  получает полный путь. WebSocket upgrade включён, timeout выключен.
  Endpoints — `Address:<порт BACKPLANE_CONSOLE_LISTEN>` здоровых
  инстансов сервиса `backplane` в каталоге, пока их нет — advertise-адрес
  этой реплики.
- **CDS** — EDS-cluster'ы по ADS на сервис, порт и протокол. Публичный
  порт SDK — cmux, выбирающий gRPC по content-type первого запроса
  соединения, поэтому соединение несёт один вид трафика: gRPC и Connect —
  cluster `<service>_grpc` с HTTP/2 upstream (gRPC-Web и JSON к этому
  моменту уже gRPC), HTTP, GraphQL и ws-proto — `<service>_http` с
  HTTP/1.1 (WebSocket — upgrade HTTP/1.1). Роут с `port` ≠ 0 идёт в
  `<service>_p<port>_<grpc|http>` — endpoints на этом порту; managed-роуты
  SDK всегда несут свой порт, так что `<service>_<grpc|http>` (порт из
  регистрации) — у declarative-роутов без `route.Port`. `_` в имени
  сервиса не бывает: сервис — часть имени до первого `_`
  (`envoy_cluster_name=~"<service>_.*"`).
- **EDS** — endpoints из каталога Consul: `Address` здоровых инстансов и
  порт cluster'а (или `Port` из регистрации; `Port` 0 — инстанс без
  публичного порта — не endpoint). Адрес не IP — инстанс пропускается с
  предупреждением (EDS принимает только IP).

Наблюдаемость: лог на каждый новый снапшот (версия, индекс каталога,
сервисы, роуты, cluster'ы), предупреждения сборки — при их изменении,
подключение и отключение Envoy (node id, cluster), каждый NACK с текстом
ошибки Envoy. Метрики узла `xds`: `backplane.xds.snapshot.version`,
`backplane.xds.services`, `backplane.xds.routes`,
`backplane.xds.clusters`, `backplane.xds.endpoints` (gauge),
`backplane.xds.streams` (открытые ADS-стримы),
`backplane.xds.errors{stage=build|snapshot|nack}`.

Балансировка, health checking upstream'ов, retries, timeouts, rate limit,
CORS, TLS, gRPC-Web/Connect-транскодинг, WebSocket — Envoy; backplane
только описывает. Envoy получает адрес backplane и node id в bootstrap
(`deployments/envoy/envoy.yaml`: admin, cluster `xds` и ADS для LDS/CDS —
остальное приходит по xDS; запуск локально — `deployments/README.md`).

В установке с Consul Connect вход обычно делает Consul API Gateway; тогда
backplane пишет роуты как config entries вместо xDS — второй драйвер
gateway, не в v0.

## 7. Хуки, активити, биндинги

### 7.1 Биндинг [backplane]

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

Сторона backplane — Nexus-handler, binding-workflow, endpoint'ы — дизайн
сервера (**[backplane]**; в conformance-тестах её играет тест, §16.3);
сторона SDK — вызов хука, worker'ы, активити — описывает код.

Своего транспорта нет. Хук — **Nexus-операция**: Nexus service = сервис
хуков вызывающего (`iam.Hooks`), операция = имя хука (`SendEmail`),
endpoint = имя сервиса (`iam`). Endpoint в реестре Temporal указывает на task queue `backplane`;
backplane — Nexus-handler: находит биндинг, исполняет его как workflow
(шаг = activity или child workflow **по имени** на очереди целевого
сервиса), возвращает результат.

Два уровня данных: **envelope — наш proto** (`backplane.HookCall{hook,
instance, trace, deadline, payload}` на входе Nexus-операции, `hook` —
полное имя `iam.SendEmail`, `trace` — W3C (`traceparent`, `tracestate`,
`baggage`); `backplane.ActivityCall{activity, trace, payload, binding,
step}` на входе активити; симметричные `*Result`); **payload — JSON** автора внутри `bytes`. CEL
работает над payload, envelope не видит. Если тип автора — proto, SDK
кладёт protojson — это тот же JSON, читаемый и в Temporal UI.

- **[backplane]** Endpoint `<service>` backplane создаёт, **как только видит манифест с
  хуками** — не при сохранении биндинга; вызов без биндинга получает
  `no binding`, а не «endpoint not found». Пока endpoint'а нет (backplane
  ещё не видел сервис), `Call` сразу возвращает `hook.ErrUnavailable`
  («no Nexus endpoint»), не дожидаясь дедлайна.
- Nexus вызывается только из workflow-кода. Внутри workflow —
  `hook.Ref.WorkflowCall(wctx, req)`: Nexus напрямую; дедлайн — `Timeout`
  вызова, иначе `DefaultTimeout` объявления, иначе платформенный
  `hook_timeout`, урезанный до остатка таймаута run'а (§3.4) — всегда
  конечный.
- Вне workflow (HTTP-handler, реактор события, `OnStart`/`OnStop`) —
  `hook.Ref.Call(ctx, req)`: SDK стартует короткий workflow
  `backplane.CallHook.v1` на **очереди хуков** `<service>.hooks`, он делает
  Nexus-вызов и возвращает результат; цена — лишний hop в миллисекунды.
  Id — `hook/<service>/<Name>/<uuid>`, с `hook.Key(k)` —
  `hook/<service>/<Name>/<k>` с политиками `WorkflowIDConflictPolicy:
  UseExisting` (идущий запуск — присоединиться) и `WorkflowIDReusePolicy:
  AllowDuplicateFailedOnly` (успешно завершённый — вернуть его результат,
  упавший — запустить заново). Дедлайн (§3.4) — он же `HookCall.deadline`,
  schedule-to-close Nexus-операции и execution timeout workflow. Memo
  запуска — `source: <service>/<instance>` (§14); search attributes
  `BpService` (`<service>`) и `BpHook` (`<service>.<Name>`) ставятся, если
  в namespace они заведены как Keyword (проверка — один раз на процесс;
  нет или нет прав оператора — вызов идёт без них).
- **Worker хуков.** `backplane.CallHook.v1` обслуживает свой worker — узел
  `hooks` сразу после `temporal`, до дерева автора: стартует раньше и
  останавливается позже него, поэтому `Call` работает из `OnStart` и
  `OnStop` компонентов. У него своя очередь, а не очередь сервиса: worker,
  опрашивающий очередь, валит workflow task'и типов, которых у него нет, —
  на одной очереди живут только worker'ы с одинаковой регистрацией. Worker
  хуков регистрирует только `backplane.CallHook.v1`, есть на каждой
  реплике, объявившей хуки, независимо от `worker.enabled`, и не
  стартует, если хуков нет. Как и основной, он не ждёт Temporal: стартует,
  как только есть соединение.
- Ошибки хука: backplane без биндинга завершает операцию non-retryable
  application error типа `backplane.NoBinding` — автор получает
  `hook.ErrNoBinding` («hook iam.SendEmail: no binding»); прочие отказы —
  сообщение обработчика без обёрток Temporal; истёкший дедлайн —
  `context.DeadlineExceeded`.
- Активити — Temporal activities сервиса на его очереди `<service>`; тип
  activity — имя активити как объявлено (`Send`), вход `ActivityCall`,
  выход `ActivityResult`; регистрирует SDK, декодирование payload в тип
  автора — в SDK; контекст обработчика несёт `activity.InfoOf` и
  `activity.Heartbeat` (§3.5). Ошибка обработчика — с префиксом
  `<service>.<Name>: `, по умолчанию retryable (ретраи — по политике шага
  биндинга); `activity.NonRetryable(err)` — non-retryable (тип
  `backplane.NonRetryable`); `*temporal.ApplicationError` автора
  передаётся как есть; не декодируемый вход — non-retryable. Паника
  обработчика — retryable ошибка `<service>.<Name>: panic: …`, запись в лог
  со стеком и метрика `backplane.panics` (`where: activity
  <service>.<Name>`): паника бывает и от временного состояния, число
  попыток ограничивает шаг. Каждое исполнение —
  `backplane.activity.duration` (`activity`, `outcome`: `ok`/`error`).
- Активити на workflow (`activity.Workflow`, `kind: WORKFLOW`) — workflow
  сервиса на его очереди, тип — имя активити, вход `ActivityCall`, выход
  `ActivityResult`; биндинг исполняет его child workflow'ом по имени.
  Ошибки — те же правила; паника в workflow-коде — как в Temporal: workflow
  task падает и повторяется, run ждёт исправленный worker.
- Trace: OTel-интерцепторы Temporal несут его заголовками через workflow,
  Nexus и activity. Если заголовки не донесли trace, активити продолжает
  trace из `ActivityCall.trace` (span со ссылкой на текущий).
- Temporal недоступен — не ошибка сервиса: SDK подключается в фоне с
  backoff, `Call` до подключения сразу возвращает `hook.ErrUnavailable`,
  worker'ы стартуют, как только есть соединение. Подключённый клиент
  проверяет соединение каждые 5 с; состояние — метрика
  `backplane.transport.connected{transport="temporal"}` (0/1) при каждом
  изменении. Readiness от Temporal по умолчанию не зависит;
  `backplane.RequireTemporal()` делает её зависящей от состояния
  соединения.
- **Раскатка.** Реплики разных версий опрашивают одну очередь сервиса, и
  Temporal отдаёт задачу любой из них:
  - новая активити (или workflow), которую биндинг вызвал, пока жива
    старая реплика: старая получает задачу незарегистрированного типа,
    попытка падает и ретраится по политике шага, пока задачу не возьмёт
    новая реплика. Биндинг на новую активити включают после раскатки или
    дают шагу retry, покрывающий её время;
  - удалённая активити — то же зеркально: снимать её из биндингов до
    раскатки версии без неё;
  - workflow вызова хука версионирован именем, `backplane.CallHook.v1`:
    код, несовместимый при replay, — новое имя (`.v2`), новые вызовы идут
    туда, а `.v1` остаётся зарегистрированным, пока в истории есть его
    незавершённые запуски (они живут не дольше дедлайна вызова). Очередь
    хуков своя: задачи `CallHook` не попадают к worker'ам активити и
    workflows автора, а их раскатка не задевает вызовы хуков;
  - workflows автора (§9) — обычная дисциплина Temporal: изменение,
    ломающее replay, — через `workflow.GetVersion` или новое имя типа.
  Для строгой изоляции версий подходят Worker Versioning / build ID
  Temporal (новая версия берёт только новые запуски, старые дорабатывают на
  старых worker'ах); SDK их не включает — рекомендация для сервисов с
  долгими workflows, настраивается в деплое.

Даром от Temporal: durability (цель лежит — шаг ретраится, сделанные шаги
не переисполняются), компенсации, async-вызов, at-least-once, access
policy endpoint'а, visibility каждого запуска со входом, выходом и шагами.

Цена: Temporal — обязательное ядро; латентность десятки миллисекунд на шаг
(хуки — бизнес-уровень, не hot path); payload до 2 MB.

Ошибки читаемы: `step send: smtp.Send: connection refused`, `no binding
for iam.SendEmail`, `transform failed at step render: <CEL>`.

## 8. События

Тонкая обёртка SDK над брокером; под капотом NATS JetStream. Что обёртка
не покрывает, сервис делает нативным клиентом: `event.JetStream(scope)`
возвращает `jetstream.JetStream` соединения сервиса (`ErrUnavailable`, если
NATS не сконфигурирован или сервис ещё не стартовал / уже остановлен; пока
соединение разорвано, клиент возвращается и переподключается сам).

- **Стрим на сервис** `bp_<service>`, subjects `bp.<service>.<Event>`
  (фильтр стрима — `bp.<service>.>`); retention и размер изолированы.
  Создаётся **идемпотентно любой стороной** — эмиттером при старте,
  подписчиком или backplane при создании consumer'а; имя и subjects
  выводятся из имени сервиса. Эмиттер (сервис, объявивший хотя бы одно
  событие) при старте делает create-or-update — его конфигурация
  побеждает; подписчик создаёт отсутствующий стрим с дефолтами платформы и
  существующий не трогает. Поэтому стрим есть до того, как эмиттер впервые
  запустился. Всегда: limits-retention, file storage, discard old. Лимиты
  задаёт оператор эмиттера в блоке `nats` (§4.3): `max_age` (7 суток),
  `max_bytes` (без ограничения; при переполнении уходят старейшие
  события), `replicas` (1), `dedup_window` (2 минуты, не больше
  `max_age`); `0` в `max_age`/`max_bytes` — без ограничения. Изменение
  применяется create-or-update при следующем старте эмиттера. Подписчик
  создаёт отсутствующий чужой стрим с дефолтами платформы (7 суток, без
  ограничения размера, 2 минуты, 1 реплика) — эмиттер при старте
  перезапишет их своими; реактор на собственное событие сервиса создаёт
  стрим сразу с лимитами своего блока `nats`. В metadata стрима —
  `bp.service`, `bp.kind` (`events`) и `bp.ensured-by` (`emitter` или
  `subscriber` — кто создал или обновил последним).
- **Имена в NATS.** Имя сервиса, события и consumer'а становится токеном
  NATS так: ASCII-буквы, цифры и `-` остаются, любой другой байт — `_XX`
  (две заглавные hex-цифры). Отображение взаимно однозначно, и в
  экранированном токене не бывает `__`; имена по конвенциям (§17) не
  меняются. Полное имя события `<service>.<Event>` делится по первой точке.
  Имя сервиса `dlq` зарезервировано под dead letters (`Connect` его
  отвергает).
- **Событие** — именованное сообщение, payload JSON (proto-тип — как
  protojson); envelope — стандартные заголовки CloudEvents, не наш proto.
  Тип автора — что угодно; SDK сериализует.
  `sent := event.Declare[MailSent](scope, "MailSent", event.Describe("..."))`,
  `sent.Publish(ctx, v, event.Key(k))`; ключ — `subject`. Имя события —
  CamelCase (`[A-Z][A-Za-z0-9]*`), иначе `Declare` паникует;
  `event.Describe` — описание события в манифесте (`events[].description`)
  и консоли. `Ref[*pb.Msg]` декодируется в новый экземпляр сообщения.
- **Совместимость payload.** Схема события меняется только добавлением
  полей. Читатель игнорирует поля, которых не знает (JSON — как
  `encoding/json`, proto — `protojson` с `DiscardUnknown`), поэтому
  эмиттер выкатывает новое поле раньше подписчиков; отсутствующее поле
  читается нулевым значением. Смысл существующего поля не меняется;
  переименование — это новое поле: эмиттер публикует оба, пока все
  реакторы не читают новое, потом старое удаляется. Те же правила — для
  входа и выхода хуков и активити (§3.4, §3.5). Payload, который реактор не
  смог декодировать, — терминальная ошибка (сразу DLQ, см. ниже).
- **Метаданные — CloudEvents** (NATS binding, binary mode, headers
  `ce-*`): `ce-specversion` = `1.0`, `ce-id` (UUID или `event.ID`),
  `ce-source` = сервис, `ce-type` = полное имя события
  `<service>.<Event>`, `ce-time` (RFC 3339 с долями секунды, UTC; момент
  публикации или `event.Time`), `ce-subject` = ключ (если задан), `ce-datacontenttype` и
  `content-type` = `application/json`; расширения `ce-instance`,
  `ce-version` и авторские `event.Header`; контекст трассировки — заголовки
  W3C `traceparent` / `tracestate` (пропагатор OpenTelemetry). `dataschema`
  в v0 не выставляется: схема события — в манифесте. `Nats-Msg-Id` =
  `ce-id`: повтор той же публикации в окне дедупликации JetStream
  отбрасывает.
- **Публикация** ждёт PubAck JetStream в пределах `ctx`; без дедлайна —
  `nats.publish_timeout` (5 с). Своей опции таймаута у `Publish` нет: срок
  задаёт `ctx` вызывающего. Пока NATS недоступен, `Publish` сразу
  возвращает ошибку (`ErrUnavailable`): SDK не буферизует и не ждёт
  переподключения. Если собственного стрима нет, SDK создаёт его и
  повторяет публикацию один раз. Опции публикации:

  | Опция | Смысл |
  |---|---|
  | `event.Key(k)` | `ce-subject`, ключ события |
  | `event.ID(id)` | `ce-id` и `Nats-Msg-Id` = `id` вместо UUID: outbox, публикующий строку повторно под её id, в окне `dedup_window` публикует её один раз; пустой id — ошибка |
  | `event.Time(t)` | `ce-time` — когда событие произошло; нулевое время — момент публикации |
  | `event.Header(name, v)` | расширение CloudEvents `ce-<name>`; `name` — строчные латинские буквы и цифры, атрибуты SDK (`id`, `source`, `type`, `time`, `subject`, `datacontenttype`, `dataschema`, `specversion`, `instance`, `version`) и значение с переводом строки не принимаются |

  Недопустимое значение опции — `Publish` возвращает `event.ErrOption`,
  ничего не отправив.
- **Подключение** не блокирует старт: недоступный NATS — предупреждение,
  клиент переподключается бесконечно (пауза 2 с плюс jitter до 1 с), стрим
  эмиттера и consumers реакторов досоздаются в фоне с backoff; разрывы и
  переподключения пишутся в лог и в метрику
  `backplane.transport.connected{transport="nats"}`. `BACKPLANE_NATS_CREDS`
  — содержимое `.creds`-файла (JWT и seed пользователя). TLS — блок
  `nats.tls` (§4.3: `enabled`, PEM-содержимое `ca`, `cert`, `key`,
  `server_name`, `insecure_skip_verify`; пустой `ca` — системный пул;
  `cert` и `key` — только вместе; ошибка в них — ошибка старта узла
  `nats`). На остановке соединение дренируется в бюджете остановки. На
  readiness NATS по умолчанию не влияет.
- **Порядок.** JetStream хранит события в порядке публикации, партиций
  нет. Реактор — durable consumer, **общий для всех реплик сервиса**:
  реплики — конкурирующие потребители, каждое событие получает одна из
  них. Поэтому при нескольких репликах или `Concurrency > 1` события
  обрабатываются параллельно и порядок, в том числе внутри одного ключа,
  не гарантирован; упавшее событие к тому же возвращается после задержки
  позади следующих. Строгий порядок — `event.Ordered()`: `max_ack_pending`
  consumer'а = 1 и `Concurrency` 1, так что во всём сервисе в полёте одно
  событие этого реактора; упавшее событие держит следующие, пока не
  пройдёт или не уйдёт в DLQ. Цена — пропускная способность одного вызова
  обработчика с сетевым круговым путём, сколько бы реплик ни было.
- **Реакторы**: `event.React[UserRegistered](scope, "iam.UserRegistered",
  handler, opts...)` = durable pull consumer, ack/nak, redelivery,
  `max_deliver` → DLQ. Имя события — `<service>.<Event>` (`[a-z0-9-]+`,
  точка, CamelCase), иначе `React` паникует. Consumer назван по пути узла и
  событию (`<путь узла>:<событие>`, на `Root` — само имя события) и уникален
  в сервисе: у каждого реактора своя позиция, несколько реакторов на одно
  событие не мешают друг другу. **Переименование или перенос компонента
  меняет имя consumer'а**: новый начинает по `StartAt`, старый остаётся на
  сервере со своей позицией (его удаляет оператор или `InactiveThreshold`).
  `event.Consumer(name)` закрепляет имя явно — тогда компонент можно
  переименовывать; чтобы сохранить существующий consumer, закрепляют его
  текущее имя. Манифест перечисляет реакторы в `subscriptions` (`event`,
  `consumer`); durable consumer в NATS — `<subscriber>__<consumer>` (оба
  экранированы, см. «Имена в NATS»; длиннее 200 символов — обрезается и
  дополняется хешем), на стриме `bp_<src>` с фильтром `bp.<src>.<Event>`;
  в metadata consumer'а — `bp.service`, `bp.consumer`, `bp.event`.
  Consumer, удалённый на сервере, пока сервис работает, SDK пересоздаёт
  с позиции реактора — со старейшего неподтверждённого сообщения, иначе
  сразу после последнего увиденного — и потребление продолжается.
  Новый consumer начинает с событий, опубликованных после его создания
  (`deliver_policy new`), или, с `event.StartAt(event.StartAll)`, со всех
  событий, которые стрим ещё хранит (`deliver_policy all`); дальше позиция
  хранится в NATS. Точка старта действует только при создании:
  существующий consumer сохраняет свою позицию, и `StartAll`, добавленный
  работающему реактору, ничего не переигрывает (для повтора истории —
  новый consumer, т. е. другое имя, или удаление consumer'а оператором).
  Подписчик декодирует своим типом (копия схемы) или динамически. Читать
  чужие стримы может любой; «подписан на всё» = consumer на каждый стрим из
  каталога манифестов, новые — по мере появления. Ограничения доступа, если
  нужны, — правами NATS-пользователя.
- **Доставка реактору** — at-least-once: обработчик может получить то же
  событие повторно и должен быть идемпотентным (`Delivery.ID` одинаков во
  всех доставках). Explicit ack; обработчик получает контекст с трассой
  события, таймаутом и метаданными: `event.DeliveryOf(ctx)` → `Delivery`
  (`ID`, `Source`, `Type`, `Subject` — ключ, `Time`, `Attempt` — номер
  доставки с 1, `Consumer` — имя из манифеста, `Extensions` — остальные
  `ce-*` без префикса: `event.Header` эмиттера, `instance`, `version`).
  Паника — ошибка (и `backplane.panics{where="reactor"}`). Успех — ack.
  Ошибка — nak с задержкой, удваивающейся с каждой доставкой от нижней
  границы до верхней; неуспешная последняя доставка — DLQ. Ошибка,
  обёрнутая `event.Terminal(err)`, — DLQ сразу, без повторов: вход, который
  повтор не исправит; payload, который не декодируется, — тоже терминальная
  ошибка. `ack_wait` = таймаут обработчика + 15 с — через столько
  возвращается сообщение, которое никто не подтвердил (процесс упал).
  Свой `BackOff` consumer'а не задан: сервер отсчитывает задержку nak от
  `ack_wait`, и вместе с `BackOff` задержки расходились бы. Решение о DLQ
  принимает SDK по номеру доставки; `max_deliver` consumer'а — опция
  `MaxDeliver` + 5 доставок запаса на прерванные остановкой (см. ниже), так
  что сообщение, прерванное на последней доставке, возвращается. Сообщение,
  которое ни разу не подтвердили (процесс падает на нём каждый раз), сервер
  перестаёт доставлять после `MaxDeliver` + 5 доставок; в DLQ оно не
  попадает — остаётся только в стриме события. Consumption переживает
  переподключения NATS. Метрика `backplane.reactor.duration{consumer,
  outcome}`: `ack`, `nak` (повтор или прерванная остановкой доставка),
  `dead` (последняя доставка), `terminal`.
- **Опции реактора** — решение автора кода, в `event.React(..., opts...)`;
  бессмысленное значение — паника при объявлении (как у `deps.Backoff`):

  | Опция | По умолчанию | Смысл |
  |---|---|---|
  | `event.MaxDeliver(n)`, `n ≥ 1` | 5 | доставок одного сообщения, первая включительно; `1` — без повторов, первая ошибка — сразу DLQ |
  | `event.Concurrency(n)`, `n ≥ 1` | 4 | обработчиков одновременно на реактор в одном инстансе (и размер выборки); `1` — по одному в инстансе, но другие реплики работают параллельно, а упавшее сообщение вернётся после задержки позади следующих |
  | `event.Ordered()` | выкл. | строгий порядок во всём сервисе: `max_ack_pending` 1, `Concurrency` 1 (см. «Порядок»); с `Concurrency(n > 1)` — паника |
  | `event.Timeout(d)`, `d > 0` | 30 с | таймаут одного вызова: контекст отменяется, вызов считается неуспешным; `ack_wait` = `d` + 15 с |
  | `event.Redelivery(min, max)`, `0 < min ≤ max` | 1 с..1 мин | задержка перед повторной доставкой: `min` после первой ошибки, удваивается до `max` |
  | `event.StartAt(event.StartNew\|StartAll)` | `StartNew` | откуда начинает **новый** consumer (см. выше) |
  | `event.Consumer(name)`, непустое | `<путь узла>:<событие>` | имя consumer'а, уникальное в сервисе (см. выше) |
  | `event.InactiveThreshold(d)`, `d > 0` | нет | сервер удаляет consumer, если `d` его не тянет ни один инстанс (убранный или переименованный реактор); удалённый так consumer при следующем старте создаётся заново по `StartAt`, события за время простоя при `StartNew` пропускаются |

  `max_deliver`, `ack_wait`, `max_ack_pending` и `inactive_threshold`
  пишутся в конфигурацию consumer'а при каждом старте реакторов
  (create-or-update), так что изменённые опции действуют после выкатки;
  точка старта — только при создании. В манифест опции не попадают: их
  видно в конфигурации consumer'а в NATS. В `backplanetest` `React`
  вызывает обработчик один раз с `Timeout` реактора и `Delivery` на
  контексте; повторов и DLQ там нет (§4.6).
- **DLQ.** Если последняя доставка (`MaxDeliver`) тоже неуспешна или
  ошибка терминальна, сообщение с исходными payload и заголовками (кроме
  служебных `Nats-*`) публикуется в `bp.dlq.<subscriber>.<consumer>` —
  стрим `bp_dlq_<subscriber>` (subjects `bp.dlq.<subscriber>.>`; подписчик
  создаёт или обновляет его при старте реакторов; хранение — `dlq_max_age`
  блока `nats`, 30 суток, `0` — без ограничения; реплики — `replicas`;
  окно дедупликации — 2 минуты, не больше хранения; metadata `bp.kind` =
  `dead-letters`) — с
  заголовками `bp-error` (текст последней ошибки, до 4 KiB), `bp-consumer`
  (имя consumer'а из манифеста) и `bp-delivered`; `Nats-Msg-Id` =
  `<durable>:<стрим>:<seq>`, так что повтор не дублирует dead letter.
  Исходное сообщение после этого терминируется (term).
- **Redrive.** `event.Redrive(ctx, scope, consumer)` прогоняет dead
  letters реактора `consumer` (имя из манифеста) через его же обработчик
  в вызвавшем инстансе, от старых к новым, с `Timeout` реактора и
  `Delivery` (`Attempt` = `bp-delivered` + 1); обработанный dead letter
  удаляется, снова упавший остаётся на месте. Dead letters, пришедшие во
  время прогона, остаются до следующего вызова. Возвращает число
  обработанных и ошибку, если какие-то упали снова. В исходный стрим
  события не публикуются заново: иначе их получили бы все подписчики
  события, а не один реактор, и повторный `ce-id` съела бы дедупликация.
  Вызывается одним инстансом (из внутреннего API или админ-команды
  сервиса): два параллельных прогона могут обработать dead letter дважды.
- **Остановка реакторов** (до остановки дерева автора): выборка
  прекращается, выбранные, но не начатые сообщения возвращаются (nak), SDK
  ждёт обработчиков в полёте до конца общего бюджета остановки (своего
  окна у узла `reactors` нет, §4.4); по его концу их контексты
  отменяются, а остановка узла — ошибка `Run`. Обработчик, прерванный
  остановкой, не считается упавшим: сообщение возвращается простым nak
  без задержки и никогда не уходит в DLQ. Такой nak уходит, когда бюджет
  уже исчерпан, и соединение NATS к этому моменту может быть закрыто —
  тогда сообщение возвращается по `ack_wait` (таймаут обработчика +
  15 с), тоже без DLQ. Счётчик доставок NATS такая доставка всё же увеличивает
  (сервер не отличает её от обычной): `Attempt` у следующей доставки на 1
  больше, а число настоящих попыток до DLQ может стать меньше
  `MaxDeliver`, но DLQ всегда следует за настоящей ошибкой обработчика.
- Метрика публикаций — `backplane.event.published{event, outcome}`:
  `ok`, `unavailable`, `timeout`, `error`.
- Интерфейс SDK узкий и брокеро-независимый: publish, durable subscribe,
  ack/nak, DLQ и его redrive; повтор истории — новый consumer со
  `StartAll`. Kafka под него встаёт; v0 — NATS.
- **[backplane]** backplane в data path событий не участвует; наблюдает
  каталог, lag, DLQ; держит consumers правил (§8.1).

### 8.1 Правила: событие → активити [backplane]

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
имя сервиса, namespace один (`BACKPLANE_TEMPORAL_NS`, по умолчанию
`default`). SDK даёт подключение, worker, расписания и настройку worker'а
из конфигурации; больше ничего своего. Логи Temporal SDK идут в логгер
сервиса, метрики и трейсы — в OTel сервиса.

- внутри сервиса — обычный Temporal: retry, таймеры, сигналы, расписания;
- между сервисами — только через хуки (§7): activity вызывает `Call` хука,
  реализация — биндинг; чужих очередей и типов сервис не знает;
- **[backplane]** из консоли — запустить любой workflow, хук или активити с входом: форма
  по схеме, если есть, иначе JSON; запуски, история, отмена, повтор —
  Temporal API в карточке.

**Регистрация и клиент.** `workflows.Register(scope, func(r
worker.Registry))` добавляет workflows и activities автора в worker
сервиса; объявление — до `Run`, после него — паника.
`workflows.Client(scope)` — Temporal-клиент сервиса (пока Temporal не
задан или не подключён — ошибка с `hook.ErrUnavailable`),
`workflows.Queue(scope)` — его task queue. Запуск своего workflow —
`ExecuteWorkflow` клиента на `Queue`.

**Объявленные workflows.** `workflows.Declare[In, Out](scope, name, fn,
workflows.Describe(s))` регистрирует `fn func(workflow.Context, In) (Out,
error)` в worker сервиса под типом `name` и записывает его в манифест
(`workflows[]`: имя, схемы входа и выхода, описание) — консоль запускает
его формой по схеме. Имя — CamelCase (§3.5), неуникальное — ошибка `Run`
(`ErrDuplicate` манифеста), объявление после `Run` — паника. Вход
приходит через конвертер Temporal по умолчанию: JSON, protojson для
proto. Объявленный через `Declare` workflow в `Register` не повторяют —
двойная регистрация в Temporal — паника.

**Worker** (identity = id инстанса) несёт activities и workflows автора и
активити сервиса (§3.5, обоих видов); создаётся, если сервис объявил
что-то из этого. Стартует после дерева автора, как только есть
соединение (неудачный старт повторяется с backoff), и останавливается до
него: `Stop` перестаёт брать задачи и ждёт идущие активити в пределах
бюджета остановки. Вызовы хуков вне workflow он не несёт: у них свой
worker на очереди `<service>.hooks`, живущий дольше дерева автора (§7.2).
Настройка — эксплуатационная, из блока `backplane.temporal.worker` (env
`BACKPLANE_TEMPORAL_WORKER_*`, §4.3), код тот же при любой:

| поле | смысл |
|---|---|
| `enabled` | `false` — реплика без worker'а: очередь сервиса обслуживают другие реплики (отдельный деплоймент-воркер), а эта держит клиент, `Call` хуков (worker хуков работает и здесь) и сверку расписаний (по умолчанию `true`) |
| `max_concurrent_activities` | activities, исполняемых одновременно |
| `max_concurrent_workflow_tasks` | workflow task'ов одновременно |
| `activity_pollers`, `workflow_pollers` | поллеры очередей activity и workflow task'ов |

`0` — умолчание Temporal SDK; отрицательное — ошибка `Open`.

**Раскатка.** Во время раскатки реплики старой и новой версии опрашивают
одну очередь сервиса; задача достаётся любой. Workflow автора, изменённый
несовместимо с replay, — через `workflow.GetVersion` или новое имя типа;
новый тип (workflow, активити) старые реплики не знают — их попытки
падают и ретраятся, пока задачу не возьмёт новая реплика, поэтому новые
типы начинают вызывать после раскатки (правила для биндингов и
`backplane.CallHook.v1` — §7.2). Для долгих workflows рекомендуется Worker
Versioning / build ID Temporal в деплое; SDK его не включает.

**Расписания** — Temporal Schedules, объявленные в коде:

```go
workflows.Schedule(root, "NightlyReport", workflows.Cron("0 3 * * *"), reports.Nightly,
    workflows.Args(reports.Daily), workflows.TimeZone("Europe/Moscow"),
    workflows.Overlap(workflows.OverlapBufferOne))
workflows.Schedule(root, "Poll", workflows.Every(time.Minute), "poller.Poll")
```

- Когда — `Cron(expr)` (как читает Temporal: 5 полей, 6 — плюс год, 7 —
  секунды первыми; `@hourly`, `@daily`, `@weekly`, `@monthly`,
  `@yearly`; в часовом поясе `TimeZone`) или `Every(d)` (интервал от
  Unix-эпохи, не меньше секунды: `Every(time.Hour)` — в начале каждого
  часа).
- Что — workflow функцией или зарегистрированным именем типа; для функции
  имя — как у `RegisterWorkflow` (короткое имя функции). `Schedule` его не
  регистрирует: workflow обязан быть в `Register` — регистрацию и
  расписание держит один автор, двойная регистрация в Temporal — паника.
- Опции: `Args(...)` — аргументы запуска (JSON; proto — protojson);
  `Overlap(p)` — что делать, если прошлый запуск ещё идёт: `OverlapSkip`
  (по умолчанию), `OverlapBufferOne`, `OverlapBufferAll`,
  `OverlapCancelOther`, `OverlapTerminateOther`, `OverlapAllowAll`; `CatchupWindow(d)` — насколько поздно
  догонять пропущенное, пока Temporal лежал (по умолчанию год);
  `Jitter(d)`; `TimeZone(iana)` (по умолчанию UTC); `Paused()` — создать на
  паузе; `PauseOnFailure()`; для запущенного workflow — `Timeout(d)`
  (execution), `RunTimeout(d)`, `Retry(policy)`.
- Имя — `[A-Za-z][A-Za-z0-9_]*`, уникально в сервисе. Плохое имя, spec,
  workflow, опция или неуникальное имя — ошибка `Run` до старта
  (`workflows.ErrSchedule`, `ErrDuplicate` манифеста); объявление после
  `Run` — паника. Манифест несёт `schedules[]`: имя, `cron` или `every`,
  тип workflow, overlap, paused, часовой пояс, jitter.
- Id расписания — `<service>/<Name>`; оно запускает workflow на очереди
  сервиса с id `<service>/<Name>-<время запуска>` (время дописывает
  Temporal), по префиксу консоль и находит запуски. Memo расписания
  `backplane.service=<service>` — владелец; memo запускаемого workflow —
  владелец и отпечаток объявления `backplane.schedule` (sha256 spec,
  workflow, аргументов, политик и таймаутов).

**Сверка расписаний.** Каждая реплика при старте, в фоне после
подключения к Temporal (узел `schedules` после worker'а; readiness не
ждёт), приводит расписания сервиса к объявленным:

- нет расписания — создаётся;
- есть — отпечаток сравнивается с объявленным; отличается — spec, действие,
  политики и пауза заменяются целиком; совпадает — не трогается, так что
  ручная пауза или снятие паузы оператором живут до изменения объявления;
- расписание с id `<service>/…` и memo-владельцем `<service>`, которого нет
  в объявлениях, удаляется; без memo-владельца (заведено руками) — не
  трогается. Сверка идёт и когда сервис не объявил ни одного расписания —
  так удаляются оставшиеся от прошлой версии; и без worker'а на реплике.

Реплики стартуют одновременно без координации: создание, наткнувшееся на
существующее расписание, превращается в сравнение отпечатков, удаление
уже удалённого — не ошибка. Временный сбой — повтор всего прохода с
backoff (1–30 с) до успеха или остановки; расписание, которое Temporal
отверг как невалидное (кривой cron, неизвестный часовой пояс), — ошибка в
лог и пропуск, остальные сверяются. Список расписаний в Temporal
eventually consistent: расписание, созданное секунды назад, сверка может
ещё не увидеть и удалит его при следующем старте. Во время раскатки
последней применяется версия той реплики, что стартовала последней.
Без Temporal объявления остаются в манифесте, в лог — предупреждение.

## 10. Прямые вызовы и mesh

Для чужих сервисов, не наших модулей: резолв `courier.service.consul` через
Consul DNS и обычный gRPC/HTTP. Ничего между ними нет; backplane не
участвует. Service mesh (sidecar, intentions, mTLS) не требуется — это
инфраструктура деплоя, её нет вне k8s. Чтобы установке с mesh ничего не
мешало: регистрация в Consul — не наша монополия (§4.2); SDK не резолвит
чужие адреса; backplane не вызывает сервисы по адресу иначе как relay
консоли; в mesh gateway-драйвер — Consul config entries (§6).

## 11. Консоль [backplane]

Раздел — сервер и фронтенд платформы; сторона SDK — секрет платформенного
порта (§4.1, §13) и отдача бандла (§3.6).

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

Консоль держит одно ws-proto соединение к backplane: `GET /ws`
(`internal/console`). На нём:

- **собственный API backplane** (`backplanepb/console/v1`, пакет
  `backplane.console.v1`), зарегистрированный через `wsrpc.GRPCRegistrar`:
  - `CatalogService` — `ListServices` (сводка: последняя версия, версии,
    инстансы, здоровые, health `HEALTHY`/`DEGRADED`/`DOWN`, есть ли
    внутреннее API и UI), `GetService` (последний манифест, манифесты всех
    версий, инстансы с состоянием и health; нет сервиса — `NOT_FOUND`),
    `WatchCatalog` (сводка сразу и после каждого нового снапшота registry;
    медленный читатель пропускает промежуточные), `ListPlugins` (§11.2);
  - `SessionService` — `ListSessions`, `RevokeSession`, `RotateToken`
    (§11.3);
  - `ConfigService` — Live-конфигурация (§5), компонент `internal/config`;
- **всё остальное — relay**: `WithUnknownHandler` по полному имени метода
  находит сервис, чей **последний** манифест (`registry.Service.Latest`)
  объявляет этот gRPC-сервис в `internal_services`, выбирает инстанс и
  пробрасывает кадры как есть (`RecvRaw → gRPC → SendRaw`), стриминг
  включительно: gRPC-поток в обе стороны с кодеком «байты как есть».
  Транскодинга нет, backplane содержимое не видит. Заголовки ответа
  сервиса уходят в браузер leading-заголовком, trailers — в END, статус
  ошибки (с details) — как есть.

Выбор инстанса: состояние инстанса есть, фаза `SERVING`, есть адрес и
платформенный порт, и манифест **его** версии объявляет этот сервис
(старые версии, где метода нет, не выбираются). Сначала — зарегистрированные
в каталоге и проходящие health check, среди них случайный; нет таких —
любой подходящий. Соединения к платформенным портам (plaintext gRPC) —
пул по адресу; адреса, которых нет в каталоге, закрываются по изменению
снапшота. Лимит сообщения — 16 MiB в обе стороны.

Правила relay:

1. пробрасываются только методы, объявленные в манифесте как внутреннее
   API; всё прочее — `PERMISSION_DENIED`; объявлен, но нет подходящего
   инстанса — `UNAVAILABLE`;
2. платформенный порт доступен только backplane (сеть); дополнительно
   backplane ставит секрет установки (`BACKPLANE_INTERNAL_SECRET` в своей
   конфигурации) в metadata `bp-internal-secret` (HTTP-заголовок
   `Bp-Internal-Secret` для бандла), SDK проверяет; без него —
   `PERMISSION_DENIED` (HTTP 403);
3. от браузера срезаются `authorization`, `cookie`, все `bp-*` и
   транспортные ключи (`:*`, `grpc-*`, `ws-*`, `content-type`, `te`,
   `host`, `connection`, `user-agent`); backplane ставит
   `bp-console-session` = id сессии соединения. Для собственного API
   backplane то же: `authorization` и `bp-*` срезаются, `bp-console-session`
   ставится, а обработчики берут сессию и из контекста
   (`console.SessionID`);
4. плагины не различаются: один администратор, один origin.

Дедлайн вызова браузера доходит до сервиса; отмена вызова браузером
отменяет вызов к сервису.

В mesh backplane — член mesh и ходит через sidecar; relay не меняется.

### 11.2 Плагины — Module Federation 2.0

Схема та же, что у Grafana и Backstage; реализация — Module Federation
2.0 (рантайм `@module-federation/enhanced/runtime`, Rspack или Vite).

- **Shell — MF-хост.** React, роутер, Mantine, UI SDK `@backplane/ui`
  объявлены `shared: singleton`. Плагин их импортирует, не бандлит.
- **Плагин — MF-remote.** `mf-manifest.json` + чанки; экспонирует
  `./Routes` и `./Nav`. Рядом наш `plugin.json`: `sdk_major`, навигация.
- **Загрузка — динамическая и ленивая.** Shell получает список плагинов
  `CatalogService.ListPlugins`: сервисы, чей последний манифест объявляет
  `ui`, с `hash`, `sdk_major`, путём бандла
  (`<база консоли>/plugins/<service>/<hash>/`) и `available` — есть ли
  живой инстанс с этим бандлом. Для совместимых и доступных shell делает
  `registerRemotes`, страницы грузит `loadRemote` при переходе на
  `/s/<service>/...`. `sdk_major` ≠ shell'у — карточка сообщает, код не
  грузится. Недоступен — плагина нет в навигации, карточка остаётся.
- **Единый UI** = один роутер, один layout (плагин рендерит только
  контент), один UI-kit из shared.
- **Доставка бандла — у сервиса.** Бандл встроен в бинарь (`embed`), SDK
  отдаёт его на платформенном порту по `GET /_backplane/ui/<path>` с
  `ETag` = `ui.hash` и `Cache-Control: no-cache` (§3.6). backplane
  раздаёт его со своего origin: `GET /plugins/<service>/<hash>/<path>`
  (только с сессией). Файл берётся у инстанса `SERVING`, манифест версии
  которого объявляет этот `ui.hash` (здоровые первыми), с
  `Bp-Internal-Secret`; ответ принимается, только если его `ETag` равен
  хэшу (иначе инстанс отдаёт другой бандл — следующий инстанс); `404`
  инстанса — `404`. Путь с хэшем не меняет содержимого, поэтому кэш —
  по `(service, hash, path)` в памяти, LRU с лимитом 64 MiB (файл больше
  четверти лимита отдаётся без кэширования), без перепроверки; одновременные
  промахи одного файла — один запрос к инстансу. Ответ:
  `Cache-Control: public, max-age=31536000, immutable`, `ETag` = хэш;
  `If-None-Match` с ним — `304`. Закэшированный файл отдаётся и без
  живого инстанса. Один origin → нет CORS, cookie работают, браузер
  сервис не видит.

### 11.3 Auth консоли

v0 — один оператор.

- **Admin-токен** — единственная учётка, хранится в
  `backplane.console_admin` (одна строка) хэшем argon2id (PHC-строка;
  m=19 MiB, t=2, p=1). Bootstrap при старте компонента консоли, когда
  строки нет: `BACKPLANE_ADMIN_TOKEN`, либо генерируется (`bpat_` + 32
  случайных байта, base64url) и печатается один раз в stderr процесса.
  Реплики стартуют конкурентно: пишет первая (`ON CONFLICT DO NOTHING`),
  печатает только она. Записанный токен главнее env: `BACKPLANE_ADMIN_TOKEN`,
  не совпадающий с ним, игнорируется с предупреждением в лог. Сброс —
  удалить строку `backplane.console_admin` и перезапустить.
- **Ротация** — `SessionService.RotateToken`: новый случайный токен,
  показывается один раз в ответе; в одной транзакции заменяет хэш и
  удаляет все сессии, кроме текущей; их соединения закрываются.
- **Вход** — `POST /auth/login` с `{"token": "..."}` → `200` с
  `{id, created_at, expires_at, idle_timeout_seconds}` и cookie
  `bp_session` (`HttpOnly; Secure; SameSite=Strict; Path=<база консоли>`).
  В cookie — случайный токен сессии (32 байта); в PostgreSQL
  (`backplane.console_session`) — только его SHA-256, плюс публичный id,
  создана, истекает, last seen, адрес, user-agent. Абсолютный срок 12 ч,
  idle 1 ч: активность — запрос с cookie или RPC на `/ws` (пишется не чаще
  раза в минуту). `POST /auth/logout` удаляет сессию и закрывает её
  соединения; `GET /auth/session` — текущая сессия или `401`.
  `SessionService.ListSessions` — живые сессии (текущая помечена),
  `RevokeSession` — удалить сессию и закрыть её соединения. Истёкшие
  сессии удаляются раз в 10 минут.
- **`/ws` upgrade** — только с живой сессией и только если `Origin` входит
  в `BACKPLANE_CONSOLE_ORIGINS` (точное `scheme://host[:port]`), а без
  списка — если host `Origin` равен `Host` запроса. Без `Origin` —
  отказ (`403`). Соединение живёт, пока жива сессия: отзыв на этой
  реплике закрывает его сразу, на другой — перечитывание сессии раз в
  30 с.
- **CSRF** — cookie `SameSite=Strict`; `/ws` проверяет `Origin`;
  `POST /auth/login` и `/auth/logout` отвергают чужой `Origin`, а без
  `Origin` — `Sec-Fetch-Site`, отличный от `same-origin`/`none`
  (клиент не из браузера не шлёт ни того, ни другого).
- **Brute force** — на адрес: 5 попыток сразу, дальше одна в 12 с;
  глобально: после 10 неудачных подряд (с любых адресов) каждый вход
  ждёт 1 с, удваиваясь с каждой следующей неудачей до 1 мин; успешный вход
  сбрасывает. Отказ — `429` с `Retry-After`. Адрес клиента — адрес пира,
  а если пир входит в `BACKPLANE_CONSOLE_TRUSTED_PROXIES` (Envoy) — первый
  справа в `X-Forwarded-For`, не входящий в список.
- **Заголовки** всех ответов: CSP `default-src 'self'; script-src 'self'`
  (без inline-скриптов; `style-src` допускает inline-стили UI-kit),
  `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`,
  COOP/CORP `same-origin`, `Strict-Transport-Security`. Ответы `/auth/*` —
  `Cache-Control: no-store`.
- **Разработка по HTTP** — `BACKPLANE_CONSOLE_INSECURE_COOKIE=true`
  убирает `Secure` у cookie и HSTS.
- **Позже** — вход через identity-сервис по OIDC; сессия остаётся
  абстракцией (`console.Sessions`). Не в v0.

Консоль ходит через Envoy: компонент `console` в дереве backplane слушает
свой адрес (`BACKPLANE_CONSOLE_LISTEN`, `:8081`) от старта узла до его
остановки (остановка закрывает listener и открытые `/ws`-соединения), а
xDS строит на него отдельный роут в cluster `backplane_console` (§6):
здоровые инстансы `backplane` на порту консоли. В манифест backplane
консоль не попадает — это не managed-роут. Один адрес на всё (`/`, `/ws`,
`/auth/*`, `/plugins/*`). Чтобы консоль не спорила с приложением за `/`,
она живёт на своём host'е (`BACKPLANE_CONSOLE_HOST`, Host-роутинг в
Envoy: `console.<domain>`) либо на префиксе (`BACKPLANE_CONSOLE_PREFIX`,
`/backplane`: Envoy передаёт путь как есть, консоль обслуживает всё под
`/backplane/`, cookie с этим `Path`) — выбор установки. `GET /` — shell;
пока он не встроен в бинарь, отдаётся заглушка.

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

## 12. Хранилище и конфигурация backplane [backplane]

### 12.1 Хранилище

Consul KV персистентен (Raft, снапшоты), но это не БД: нет истории,
запросов кроме get/list, транзакций сверх `txn` на 64 операции, значение
≤ 512 KB. Temporal и так требует PostgreSQL — backplane использует **тот же
инстанс, свою схему `backplane`**.

| где | что |
|---|---|
| **PostgreSQL**, схема `backplane` | биндинги, правила; ревизии Live-значений; сессии консоли; хэш admin-токена; аудит |
| **Consul KV** | манифесты и состояние инстансов — proto binary (пишет SDK); `config/<service>/` — доставка Live-значений: скаляр — текстом, контейнер — JSON, плюс `_revision` (пишет backplane, §5.3) |
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
| `BACKPLANE_PG_DSN` | обязателен; схема `backplane` — создаётся и мигрируется при старте |
| `BACKPLANE_CONSUL_ADDR` | обязателен для backplane (у сервисов — опционален) |
| `BACKPLANE_XDS_LISTEN` | ADS для Envoy (`:18000`) |
| `BACKPLANE_XDS_HTTP_PORT` | порт публичного listener'а, который описывает снапшот (Envoy его слушает; `10000`) |
| `BACKPLANE_CONSOLE_LISTEN` | HTTP консоли (`/`, `/ws`, `/auth`, `/plugins`) — за Envoy (`:8081`) |
| `BACKPLANE_ADMIN_TOKEN` | bootstrap консоли (опционально) |
| `BACKPLANE_CONSOLE_HOST` или `_PREFIX`, `_ORIGINS`, `_TRUSTED_PROXIES` | консоль; host и prefix взаимоисключающие, списки — JSON |
| `BACKPLANE_CONSOLE_INSECURE_COOKIE` | `true` — cookie сессии без `Secure` и без HSTS: только разработка по HTTP (`false`) |
| `BACKPLANE_OBS_METRICS_URL`, `_LOGS_URL`, `_TRACES_URL` | observability (опционально) |
| `BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL` | период прохода reconciler'а `config/` сверх прохода на старте и по изменению KV (`30s`, больше 0; §5.3) |

## 13. Безопасность (v0)

Модель доверия: одна установка, доверенная внутренняя сеть, один
оператор. Ничего своего — у каждого компонента родной auth, креды выдаёт
деплой в конфигурации.

1. **Границы.** Внешний периметр — Envoy; TLS-терминация там или выше
   (LB, ingress) — решает деплой. Внутренние порты наружу не публикуются.
   Внутри plaintext по умолчанию; каждый клиент SDK — Consul, NATS,
   Temporal — умеет TLS через конфигурацию (`config.TLS`: PEM-содержимое из
   секретов, не пути к файлам; клиентский сертификат — парой).
2. **Consul ACL.** Токен на сервис: регистрировать себя, читать каталог,
   писать свои манифесты и состояние, читать все манифесты (для «подписан
   на всё»), читать свой `config/<name>/`. Токен
   backplane: читать каталог и `backplane/*`, писать `config/*`. Шаблонов
   политик в репозитории нет — их пишет деплой. В dev без ACL работает.
3. **NATS.** Пользователь на сервис: publish `bp.<name>.>` и
   `bp.dlq.<name>.>`, subscribe `bp.>`, JetStream API на свои стримы
   (`bp_<name>`, `bp_dlq_<name>`), на создание отсутствующих `bp_*`
   (подписчик создаёт стрим источника) и на свои consumers `<name>__*`.
   backplane — читать всё, JS API для consumers правил. В dev без auth
   работает.
4. **Temporal.** Один namespace; per-service авторизация в OSS требует
   authorizer-плагина — в v0 нет, доверие внутри namespace явное. Nexus
   endpoint access policy — allowlist namespace. Temporal Cloud — API key
   (`BACKPLANE_TEMPORAL_API_KEY`, §4.3) поверх TLS.
5. **backplane → платформенный порт сервисов.** Сеть плюс
   `BACKPLANE_INTERNAL_SECRET` в конфигурации обеих сторон. Пустой секрет
   выключает проверку: внутреннее API и бандл открыты всем, кто достал до
   порта, — `Run` предупреждает об этом в лог. Пробы и `grpc.health.v1`
   секретом не закрыты. SDK сравнивает секрет за постоянное время
   (HTTP и gRPC одинаково).

   **Ротация секрета** без простоя: (1) сервисам — новый секрет в
   `BACKPLANE_INTERNAL_SECRET`, старый — в `BACKPLANE_INTERNAL_SECRET_PREVIOUS`
   (rollout; оба принимаются); (2) backplane переходит на новый;
   (3) сервисам убрать `_PREVIOUS` (rollout). `_PREVIOUS` без основного
   секрета проверку не включает.
6. **Секреты.** Поле типа `config.Secret` маскируется везде, где его
   выводит SDK (fmt, лог, JSON, эффективная конфигурация в состоянии
   инстанса, схема — пометка `secret`). **[backplane]** В карточке и
   истории — по пометке `secret` в схеме, без схемы — предупреждение и
   показ как есть. Live-значения лежат в PostgreSQL и KV открытым текстом
   под защитой доступа; шифрование at rest — не в v0.
7. **[backplane] Консоль** — §11.3; CSP `script-src 'self'`, без inline.
8. **[backplane] Хуки** — авторизация = биндинг: вызывается только
   привязанное.

Не в v0: несколько пользователей, RBAC, mesh/mTLS между сервисами,
per-service auth в Temporal, шифрование at rest.

## 14. Аудит [backplane]

Сторона SDK — memo `source` и search attributes запуска хука (§7.2);
остальное — сервер.

Принцип: не дублировать то, что стек уже пишет.

| что | где | что видно |
|---|---|---|
| запуск хука, биндинга, правила | Temporal history | вход, выход, шаги, ошибки, время; кто — в memo (`source: iam/<instance>` у `Call` вне workflow, `console: <session>` у запуска из консоли); поиск — по search attributes `BpService`, `BpHook`, если namespace их завёл (Keyword; без них SDK вызывает без атрибутов) |
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
  `trace_id`, экспорт по стандартным `OTEL_*`; каждый узел дерева — свой
  трейсер и метр (`<service>/<путь>`);
- Temporal — OTel-интерцепторы: workflow, activity, Nexus-операция — хук
  из IAM → биндинг → activity в smtp = один trace;
- NATS — `traceparent` в CloudEvents;
- backplane — сам сервис: relay, reconciler, правила — span'ы.

**Resource-атрибуты ставит SDK**: `service.name`, `service.instance.id`,
`service.version`, `deployment.environment.name` (если задан
`BACKPLANE_ENVIRONMENT`). Автор ничего не пишет.

### 15.4 Что пишет SDK

Метрики SDK — метр `github.com/gopherex/backplane`:

| метрика | тип | атрибуты | когда |
|---|---|---|---|
| `backplane.dependency.provide.attempts` | counter | `node`, `outcome` (`ok`, `error`) | каждая попытка `Provide` обязательной или опциональной зависимости |
| `backplane.dependency.ready` | gauge 0/1 | `node` | значение предоставлено (и проходит пробу при `ProbeOptional`) |
| `backplane.config.updates` | counter | `outcome` (`applied`, `rejected`) | обновление конфигурации из KV (§5.5) |
| `backplane.config.degraded` | gauge 0/1 | — | слой Consul пуст или устарел |
| `backplane.hook.call.duration` | histogram, s | `hook`, `outcome` (`ok`, `no_binding`, `unavailable`, `timeout`, `error`) | каждый `Call` хука |
| `backplane.event.published` | counter | `event`, `outcome` (`ok`, `unavailable`, `timeout`, `error`) | каждый `Publish` |
| `backplane.reactor.duration` | histogram, s | `consumer`, `outcome` (`ack`, `nak`, `dead`, `terminal`) | каждая доставка реактору и каждый dead letter в `Redrive` |
| `backplane.activity.duration` | histogram, s | `activity` (`<service>.<Name>`), `outcome` (`ok`, `error`) | каждое исполнение `activity.Handle` |
| `backplane.transport.connected` | gauge 0/1 | `transport` (`consul`, `nats`, `temporal`) | при каждом изменении соединения |
| `backplane.panics` | counter | `where` (`grpc.public`, `grpc.internal`, `wsproto`, `http.public`, `http.platform`, `reactor`, `activity <service>.<Name>`) | каждая перехваченная паника |

Кроме них: метрики хоста и Go-рантайма (xtrace), метрики Temporal SDK
(метр `github.com/gopherex/backplane/temporal`), otelgrpc и otelhttp
серверов.

Span'ы SDK:

- gRPC-серверы обоих классов портов — otelgrpc (кроме `grpc.health.v1`);
  HTTP публичных портов — otelhttp (операция `public<addr>`); ws-proto —
  server span на вызов (имя — полный метод, `rpc.system` = `ws-proto`).
- `publish <service>.<Event>` (producer) на каждый `Publish`; контекст
  уходит в `traceparent` события. `process <service>.<Event>` (consumer)
  на каждую доставку реактору, в trace события; атрибуты `messaging.*`
  (система `nats`, subject, durable, номер доставки).
- Temporal — интерцепторы OTel SDK Temporal: запуск `CallHook`, workflow,
  Nexus-операция, activity. `activity <service>.<Name>` (consumer) — когда
  заголовки Temporal не донесли trace, активити продолжает trace из
  `ActivityCall.trace` со ссылкой на текущий span.
- Узлы дерева — `scope.Span` / `deps.SpanValue` на трейсере
  `<service>/<путь>`.

### 15.2 Что показывает карточка [backplane]

Всё по конвенциям, без настройки: метрики с `service="<name>"` (список
через label API, графики по инстансам; сверху — внешнее API из метрик
Envoy, хуки и правила из метрик Temporal, lag событий из JetStream); логи
по `service`/`instance` с переходом по `trace_id`; трейсы по
`service.name` с waterfall и переходом в логи; explore с предзаполненным
контекстом.

Компоненты — из HyperDX (timeseries, логи, waterfall, stat), адаптированные
под Mantine и UI SDK.

### 15.3 Драйверы и что должен обеспечить деплой [backplane]

backplane — query-proxy: ходит в endpoint'ы со своей аутентификацией,
подставляет контекст сервиса.

| драйвер | метрики | логи | трейсы |
|---|---|---|---|
| **victoria** (v0) | VictoriaMetrics, PromQL/MetricsQL | VictoriaLogs, LogsQL | VictoriaTraces, Jaeger-совместимый API |
| prometheus/loki/tempo (позже) | PromQL | LogQL | TraceQL |

Панели Envoy, Temporal и NATS показывают что-то, только если деплой
скрейпит их метрики в тот же backend. Что скрейпить (Envoy
`/stats/prometheus`, Temporal server, NATS exporter), какие лейблы ждём,
куда слать OTLP — `deployments/README.md`. Не сделали — панели пустые,
остальное работает.

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

Статус: M0 сделан; из M2 сделана сторона SDK; сервер backplane (M1, M2,
M3) не начат.

**M0 — SDK и `hello`, без backplane. Сделано.** Platform-in-a-box:
compose с Consul, NATS, Temporal (dev-сервер с Nexus), PostgreSQL и Envoy
(bootstrap ждёт xDS backplane). SDK: конфигурация из env/файла с
Live-полями из KV и `Validate`, дерево узлов с зависимостями и бюджетом
остановки, манифест и состояние инстанса в KV, регистрация в Consul,
платформенный порт (cmux: внутреннее API за секретом и гейтом, health,
xprobe, бандл, info, pprof), managed- и declarative-роуты с политикой
Envoy, OTel (resource, серверы, логи с trace_id), `backplanetest`.
`hello` объявляет все разделы манифеста. Доказывает: сервис живёт без
backplane; манифест и слои конфига верны.

**M1 — backplane: конфиг, gateway, консоль. Не начат.** Watch каталога
и KV; схема PostgreSQL, ревизии, репликация в KV (hot reload в SDK уже
есть); xDS из роутов `hello`; shell с карточками (инстансы, конфигурация
по инстансам с историей Live-значений, внешнее API); relay ws-proto →
внутреннее API; auth консоли. Доказывает: цикл «форма → ревизия → KV →
сервис» и вход через Envoy.

**M2 — хуки, биндинги, правила, события.** Сторона SDK **сделана**:
Temporal в SDK — хуки (`Call` через `backplane.CallHook.v1` на очереди
`<service>.hooks`, `WorkflowCall` — Nexus напрямую, ключ идемпотентности),
активити обоих видов, workflows (`Register`, `Declare`, `Client`) и
расписания со сверкой, настройка worker'а; события CloudEvents над
JetStream, реакторы с опциями доставки, DLQ и `Redrive`. Сторона
backplane **не начата**: Nexus-endpoint и handler, binding-workflow, DSL
с CEL над JSON, правила. `hello` объявляет хук `Greet` (вызывается из
HTTP-handler'а и из workflow), активити `Echo` и событие `Greeted`;
биндинг `hello.Greet := hello.Echo`, правило `on hello.Greeted :=
hello.Echo` — на стороне backplane. Доказывает: развязка end-to-end, один
trace через всё.

**M3 — плагины, observability, аудит. Не начат.** UI SDK, MF-хост,
доставка бандла консолью (отдача с платформенного порта в SDK есть),
плагин `hello`; драйвер `victoria`; аудит и событие
`backplane.AuditEntry`.

**Дальше, вне репозитория** — Kratos-wrapper, `template`, `smtp` (courier
режется на атомарные способности), биндинг `iam.SendEmail`. Любой special
case в SDK ради них — дефект модели.

### 16.3 Эталонные сервисы, conformance, шаблон

- В репозитории один пример — `examples/hello`; он же и вызывает хук, и
  реализует активити (`hello.Greet := hello.Echo`). Ничего доменного.
- **Conformance** (`conformance/`, против `make up`; запуск —
  `deployments/README.md`) —
  тесты над контрактом SDK снаружи: по сети, Consul, NATS и Temporal.
  Каждый набор включается своей переменной и без неё пропускается:
  `BACKPLANE_TEST_CONSUL`, `BACKPLANE_TEST_NATS`,
  `BACKPLANE_TEST_TEMPORAL`. Сторону backplane (Nexus endpoint, handler,
  binding-workflow) там играет сам тест. Проверяется:
  - манифест и состояние инстанса в KV, регистрация и check в каталоге,
    уход из каталога и KV на остановке;
  - платформенный порт: health открыт, внутреннее API и бандл за
    секретом; бандл с `ETag` = `ui.hash`, `Cache-Control: no-cache` и
    `304` на `If-None-Match`;
  - внешнее API: gRPC, стрим, HTTP, ws-proto;
  - слои конфига и hot reload по записи в KV (источник KV в состоянии);
  - хук из HTTP-handler'а и из `OnStart`/`OnStop` через биндинг, который
    вызывает активити по имени с JSON-входом; `hook.Key` исполняет
    биндинг один раз; `no binding`;
  - хук из workflow (`WorkflowCall`); активити на workflow по имени с
    envelope `ActivityCall`;
  - событие с корректными `ce-*` и trace до реактора;
  - реакторы через `backplane.Open`: DLQ по терминальной ошибке и после
    последней доставки, заголовки dead letter, `Redrive`; обработчик,
    прерванный остановкой, возвращает сообщение без DLQ; эволюция схемы
    (лишнее поле — игнор, JSON и protojson; отсутствующее — нулевое);
  - workflows и расписания: клиент, создание, обновление, удаление
    неописанного на реплике без worker'а.

  **[backplane]** Не проверяется, потому что это сторона сервера: relay
  внутреннего API через консоль, правило и его дедупликация по `ce-id`.
  Другой язык SDK проходит этот же набор.
- **Шаблон сервиса** — один (не реализован); генерирует `proto/`,
  `internal/`, `cmd/`, `ui/`, Makefile, Dockerfile, conformance-таргет;
  версия шаблона записывается; `template upgrade` — v1.

### 16.4 Известные риски

- **[backplane] Регистрация Nexus-операций.** В Temporal Go набор
  операций фиксируется до старта worker'а. backplane — один handler для
  хуков всех сервисов: новый хук → пересобрать набор и перезапустить
  worker (объект в процессе). Первое, что проверить на стороне сервера
  M2: нет ли catch-all и не рвёт ли перезапуск in-flight.
- **Активити по имени.** Единственное место, где backplane формирует
  данные для чужого кода: envelope `ActivityCall` наш, payload — JSON из
  CEL. Сторону SDK conformance проверяет, играя backplane (§16.3).
- **[backplane] HyperDX-компоненты** не публикуются как библиотека —
  извлечение и адаптация под Mantine, работа на M3.

## 17. Конвенции имён

| что | как |
|---|---|
| имя сервиса | `^[a-z][a-z0-9-]*$` (иначе `backplane.ErrConfig` из `Open`), уникально в установке; `dlq` зарезервировано (узел `nats` его отвергает) |
| id инстанса | `<service>-<hostname>` (или `BACKPLANE_INSTANCE`, опция `backplane.Instance`) = Consul service ID = `service.instance.id` = identity Temporal-клиента |
| Consul: регистрация | имя — сервис; адрес `BACKPLANE_ADVERTISE` / `POD_IP` / hostname; `Port` — основной публичный порт (0 без внешнего API); теги `BACKPLANE_CONSUL_TAGS`; gRPC check на платформенный порт; `Meta` не заполняется |
| Consul: сессия | `<instance>#<инкарнация>`, `LockDelay` 1 мс, TTL `session_ttl` |
| Consul KV | `backplane/services/<service>/manifests/<version>` (dev-версия `0.0.0` — `0.0.0+<12 hex хэша манифеста>`), `backplane/services/<service>/instances/<id>`, `config/<service>/<path>` — только пути Live-полей (вложенность — `/`; скаляр — строкой, контейнер — JSON); ревизия — `config/<service>/_revision` (не Live-путь: SDK не применяет его как значение) |
| env | `<SERVICE>_<PATH>`: `SERVICE` — имя сервиса в верхнем регистре, `-` и `.` → `_`; путь — JSON-имена полей в верхнем регистре через `_`; блок SDK — `BACKPLANE_<PATH>` (таблица §4.3) |
| файл | `BACKPLANE_CONFIG_FILE` (YAML/JSON) той же формы, что структура конфигурации |
| платформенный порт | `/healthz/liveness`, `/healthz/readiness`, `/healthz/startup`, `/_backplane/ui/<path>`, `/_backplane/info`, `/debug/pprof/*`; секрет — gRPC metadata `bp-internal-secret`, HTTP `Bp-Internal-Secret` |
| NATS | стрим `bp_<service>`, subject `bp.<service>.<Event>` (фильтр стрима `bp.<service>.>`); durable consumer реактора `<subscriber>__<consumer>` (`consumer` — `subscriptions[].consumer` манифеста: `<путь узла>:<событие>`, на `Root` — `<событие>`, или `event.Consumer`); DLQ — subject `bp.dlq.<subscriber>.<consumer>`, стрим `bp_dlq_<subscriber>`; имена экранируются (§8); metadata стримов `bp.service`, `bp.kind` (`events` / `dead-letters`), `bp.ensured-by`, consumer'ов `bp.service`, `bp.consumer`, `bp.event`; заголовки dead letter `bp-error`, `bp-consumer`, `bp-delivered`; **[backplane]** consumers правил `backplane__rule_<id>` |
| Temporal | namespace один (`BACKPLANE_TEMPORAL_NS`); task queue сервиса `<service>`; очередь вызовов хуков `<service>.hooks`; Nexus endpoint `<service>`, Nexus service `<service>.Hooks`, операция `<Name>`; activity type — имя активити; workflow type активити на workflow и `workflows.Declare` — объявленное имя; workflow вызова хука вне workflow `backplane.CallHook.v1`, id `hook/<service>/<Name>/<uuid>` или `hook/<service>/<Name>/<key>`, memo `source` = `<service>/<instance>`, search attributes `BpService`, `BpHook`; расписание `<service>/<Name>`, его запуски — workflow id `<service>/<Name>-<время>`, memo `backplane.service`, `backplane.schedule`; типы application error: `backplane.NoBinding` (нет биндинга), `backplane.HookFailed`, `backplane.Timeout` (вызов хука), `backplane.NonRetryable` (активити); **[backplane]** очередь backplane `backplane`, workflow id правила `rule/<id>/<ce-id>` |
| имена хуков, активити, событий, workflows | `<service>.<Name>`, `Name` — CamelCase `[A-Z][A-Za-z0-9]*`; имя расписания — `[A-Za-z][A-Za-z0-9_]*` |
| proto-пакеты | внутреннее API — `<service>.console.v1` (`-` → `_`; другой пакет — ошибка `Run`); конвенция генератора, SDK её не проверяет: хуки `<service>.hooks.v1`, активити `<service>.activities.v1`, события `<service>.events.v1` |
| **[backplane]** консоль | под базой консоли (`/` или `<prefix>/`): `/auth/login`, `/auth/logout`, `/auth/session`, `/ws`, `/plugins/<service>/<hash>/<path>` — бандл, `/s/<service>/...` — плагин в shell; cookie сессии `bp_session`; собственный API — proto-пакет `backplane.console.v1`; relay ставит metadata `bp-console-session` (id сессии) и `bp-internal-secret` |

## 18. Раскладка репозитория

Что есть:

```
backplane/
  platform-design.md         этот документ
  Makefile  easyp.yaml  easyp.lock  sqld.yaml  go.mod  .golangci.yaml
  docker-compose.yaml        platform-in-a-box: Consul, Envoy, NATS, Temporal (dev), PostgreSQL
  deployments/               что даёт деплой: README для девопсов (§4.3, §15.3), envoy/envoy.yaml —
                             bootstrap Envoy (admin 9901, xDS ADS от backplane на 18000)
  backplanepb/v1/            proto-контракт backplane.v1 (manifest, instance, call) и сгенерированный Go
  backplanepb/console/v1/    API консоли backplane.console.v1 (catalog, session, config) и сгенерированный Go
  cmd/
    backplane/               бинарь платформы: сервис на своём SDK (имя backplane)
  internal/                  приватное backplane (сервер):
    server/                  конфигурация сервера (§12.2, Validate) и State: дерево узлов — store,
                             registry, дальше компоненты; readiness и объявления сервера
    store/                   PostgreSQL, схема backplane: pgxpool + pgtx (транзакции), миграции sqld
                             при старте (migrations/, из diff schema.sql), типизированные запросы
                             (queries/ → db/, sqld-gen-go)
    registry/                каталог установки из Consul: blocking queries на каталог, health
                             сервисов и backplane/services/; неизменяемые снапшоты Catalog, Source
                             (Current, Changes), Hub — источник снапшотов и фейк для тестов
    config/                  Live-конфигурация (§5.2, §5.3): ревизии override в PostgreSQL, валидация
                             на инстансах схемой, доставка в config/<service>/ (txn + CAS на _revision),
                             reconciler, ConfigService консоли (Manager.API)
    xds/                     control-plane Envoy (§6): ADS-сервер на своём адресе, снапшот из Catalog
                             (Build: listener, маршруты, cluster'ы, endpoints), метрики, NACK в лог
    console/                 консоль (§11): компонент со своим listener'ом — вход по
                             admin-токену (argon2id), сессии (Sessions: PostgreSQL, PG), brute force,
                             /ws (ws-proto: CatalogService, SessionService, ConfigService; relay во
                             внутреннее API сервисов), бандлы плагинов с LRU-кэшем, заголовки безопасности
  pkg/backplane/             Go SDK: Open, Root, Service, Identity, Info, опции Open, ErrConfig, ErrClosed
    activity/                активити: Handle, Workflow, опции для биндинга, InfoOf, Heartbeat, NonRetryable
    backplanetest/           harness для тестов компонентов без Open/Run/портов/Consul (§4.6)
    build/                   идентичность из ldflags и build info
    config/                  конфигурация: Load, Open, Runtime, Backplane, Live, Secret, TLS, Validator
    deps/                    дерево узлов: Scope, Component, Dependency, Optional, Singleton,
                             Provider, Factory, Func (без зависимости от пакета backplane)
    event/                   события: Declare, Publish и опции, React и опции доставки, Delivery,
                             Terminal, Redrive, JetStream
    hook/                    хуки: Declare, Call, WorkflowCall, Key, Timeout, ErrUnavailable, ErrNoBinding
    route/                   declarative-роуты, опции managed-роутов, политика Envoy, Origins
    workflows/               workflows автора: Register, Declare, Client, Queue, Schedule и опции
    wsproto/                 ws-proto как managed-роут (своя зависимость ws-proto)
    internal/
      backoff/               политика ретраев (экспоненциальная, с джиттером)
      broker/                события над NATS JetStream: стримы, публикация, реакторы, DLQ, redrive
      configrt/              что ядру нужно от живой конфигурации: эффективные значения, источники,
                             схема, общий Consul-клиент
      consul/                присутствие: манифест, состояние инстанса под сессией, регистрация
      decl/                  общее для объявлений: сервис узла, схемы, кодирование payload
      env/                   общее для узлов одного сервиса: манифест, транспорты, обработчики
      gate/                  гейт внутреннего API: пускает, пока поднято дерево автора
      guard/                 секрет платформенного порта (gRPC и HTTP)
      health/                пробы, grpc.health.v1, HTTP-пробы; один источник для Consul check
      link/                  доступ SDK-пакетов к приватному публичных типов
      listener/              один адрес: gRPC и HTTP через cmux, остановка в бюджете
      manifest/              сборка манифеста, общий FileDescriptorSet, ui.hash
      metrics/               метрики SDK (§15.4)
      node/                  дерево узлов и lifecycle: старт в порядке создания, стоп обратно
      recovery/              паника handler'а — ошибка запроса, backplane.panics
      routes/                описания managed-роутов: префиксы, хосты, перехватчики, политика
      telemetry/             OTel через xtrace: resource, экспортёры из OTEL_*
      temporal/              Temporal: соединение, вызов хуков, worker'ы, активити, расписания, TLS
        temporaltest/        тестовая обвязка против dev-сервера: играет Nexus-сторону backplane
      testlog/               логгер для тестов
  examples/
    hello/                   единственный пример: proto/, internal/, cmd/, ui/
  conformance/               тесты контракта SDK против platform-in-a-box (§16.3)
```

**[backplane]** Чего нет — остальное сервера и фронтенд платформы, по плану:

```
  cmd/
    protoc-gen-backplane/    генератор (удобство)
  internal/                  xds, nexus (handler и binding-workflow), rules,
                             console (ws-proto server, relay, auth), obs (query-proxy)
  web/                       yarn workspace
    packages/console/        shell (MF-хост)
    packages/ui/             @backplane/ui — UI SDK для плагинов, включая @backplane/ui-build
```

`pkg/` — только то, что импортируют сервисы; всё остальное Go — в
`internal/`. Корень — документ, сборка, compose.

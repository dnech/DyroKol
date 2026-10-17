# Разработка

## Требования

- **Go 1.26+** (в `go.mod` указана `go 1.26.0`; проект собирался и проверялся на Go 1.27).
- CGO **не нужен**: SQLite — чистый Go (`modernc.org/sqlite`). Исключение — тесты с детектором
  гонок (`-race`), которым нужен `gcc`.
- Для проверки интерфейса: Python 3 с Playwright и Chromium (необязательно).
- `shellcheck` для скриптов (необязательно).

## Сборка

```bash
go build ./cmd/dyrokol            # внимание: пишет файл ./dyrokol в текущий каталог
go build -o /tmp/dyrokol ./cmd/dyrokol
```

Все платформы сразу:

```bash
scripts/build.sh                  # все цели
scripts/build.sh linux-amd64      # одна или несколько целей по имени
```

| Цель | GOOS / GOARCH |
| --- | --- |
| `linux-amd64` | linux / amd64 |
| `linux-arm64` | linux / arm64 |
| `linux-arm` | linux / arm (GOARM=7) |
| `termux-arm64` | linux / arm64 |
| `termux-arm` | linux / arm (GOARM=7) |
| `windows-amd64` | windows / amd64 |
| `windows-arm64` | windows / arm64 |

Сборки статические (`CGO_ENABLED=0`), `-trimpath`, без отладочной информации (`-s -w`); каждая
весит 12–13 МБ. Интерфейс встроен в бинарник через `go:embed`, поэтому **после правки файлов в
`internal/server/web` бинарник нужно пересобрать**.

`cmd/dyrokol/rsrc_windows_*.syso` — ресурсы Windows (иконка), подключаются автоматически при
сборке под Windows.

Служба Windows (`dyrokol service ...`) живёт в `cmd/dyrokol/service_windows.go`; на остальных
платформах команда сообщает, что не поддерживается (`service_other.go`). Проверить, что код
собирается под Windows, можно с Linux: `GOOS=windows go vet ./cmd/...`.

Бинарники **не хранятся в репозитории** (`bin/` и `dist/` в `.gitignore`): они публикуются как файлы
GitHub Release, см. [Выпуск](#выпуск).

## Тесты

```bash
go vet ./...
go test ./...                       # все пакеты
CGO_ENABLED=1 go test -race ./...   # с детектором гонок (нужен gcc)
go test -run TestEndToEnd ./internal/server/
```

Основной объём — в `internal/server` (сквозные сценарии на настоящих relay и агентах внутри
процесса теста) и `internal/store`.

| Пакет | Что покрыто |
| --- | --- |
| `internal/server` | Сквозной сценарий (регистрация, порты, маршрут, статистика, рестарт и автоматическая разблокировка, удаление); вход на relay + выход на агенте; выход на второй машине; SOCKS5; защита токеном и `Origin`; фильтр источника; бэкап и восстановление; **проверка принадлежности порта в route-стриме**; точность счётчиков; события в журнале; один порт на разных машинах; кэш маршрутов; несколько входных портов на одну выходную точку `PUT /api/endpoints/{id}/routes`, смена номера relay-порта с откатом, `PATCH /api/routes/{id}`, включение и выключение маршрута, API раскладки схемы. |
| `internal/store` | Шифрование и отсутствие открытого текста, round-trip, каскадное удаление, защита последнего admin, валидация, ротация ключа, уникальность порта по машине, бэкап/восстановление, фильтр источника без портов, атомарная замена набора входных портов точки (`SetEndpointRoutes`), правка номера порта и цели точки, `UpdateRoute`, раскладка схемы (проверки, сохранение, бэкап с переназначением id). |
| `internal/agent` | Идентификаторы портов разных relay не пересекаются, перенос порта, отклонение некорректного push, сброс счётчиков в отчёте. |
| `internal/traffic` | Дельты счётчиков и пиров, учёт трафика в `Relay`, `ConnSet.Shut`. |
| `internal/filter` | Семантика allow/deny, разбор шаблонов, `ValidateSource`. |
| `internal/config` | Каталог данных и приоритет значений. |

Тесты используют свободные порты на `127.0.0.1` и временные каталоги, поэтому безопасно
запускаются параллельно с вашей работой.

## Стенд для разработки интерфейса

`scripts/devrig.sh` поднимает relay, админ-агента, тестовый веб-сервер и наполняет демо-данными
(несколько машин, портов, точек и маршрутов):

```bash
go build -o /tmp/dyrokol ./cmd/dyrokol
scripts/devrig.sh /tmp/dyrokol        # админка: http://127.0.0.1:19180, токен: uitoken
scripts/devrig.sh stop
```

Поскольку UI встроен в бинарник, после правок пересоберите и перезапустите стенд.

Автоматические проверки интерфейса (нужны Python-пакет `playwright` и `/usr/bin/chromium`):

```bash
python3 scripts/uiflows.py            # сценарии: вход, схема, создание порта, дубликат, маршруты на странице Routes
                                      # (в том числе несколько входных на одну точку), правка номеров портов, сохранение раскладки схемы
                                      # (перетаскивание, рамка, подпись, слои), масштаб/панорама/полный экран, удаление, замок
python3 scripts/uishots.py /tmp/shots # скриншоты всех страниц: тёмная, светлая, мобильная
python3 scripts/docshots.py           # пересоздать скриншоты для документации (docs/img/)
```

`uishots.py` печатает ошибки консоли браузера и исключения страницы, если они были.

## Структура проекта

```
cmd/dyrokol/             CLI (main.go), служба Windows (service*.go), ресурсы Windows
internal/
  agent/                 агент: agent.go, ingress.go, admin.go, config.go
  server/                relay: server.go, handshake.go, session.go, ingress.go, push.go,
                         flush.go, auth.go, api*.go; web/ — встроенный UI
  store/                 sealed.go, store.go, model.go, mesh.go, runtime.go, backup.go
  protocol/ transport/ traffic/ copyio/ egress/ socks5/ filter/ config/
docs/                    документация: ru/ и en/, скриншоты в img/
scripts/                 build.sh, release.sh, install.sh, devrig.sh, uiflows.py, uishots.py, docshots.py
assets/                  иконка приложения
bin/, dist/              результат сборки и релизные файлы (в .gitignore)
```

Подробно — в [Архитектуре](architecture.md).

## Соглашения

- `gofmt` и `go vet` должны быть чисты; скрипты проходят `shellcheck`.
- Комментарии объясняют **зачем**, а не пересказывают код; у каждого пакета есть doc-комментарий.
- Ошибки хранилища — типизированные (`store.Error`), HTTP-статус выбирается в одном месте
  (`writeStoreError`).
- Любое изменение конфигурации — через `store.mutate` и затем `server.configChanged()`.
- Новые тесты на найденные дефекты кладите рядом с исправлением (`regress_test.go`).

## Как добавить…

### …поле или сущность в конфигурацию

1. Схема и модель: `store/store.go` (`configSchema`), `store/model.go` (тип, сканер, `normalize`).
2. Операции: `store/mesh.go`. Меняют конфигурацию — только внутри `s.mutate`.
3. Бэкап: `store/backup.go` (`Snapshot`, `Import`, `validate`).
4. **Повысьте `schemaVersion`** (миграций нет) и версию в снимке бэкапа обновится сама.
5. Доставка на агентов — `server/push.go` (`buildConfig`) и `protocol.ConfigPush`; если меняется
   формат — повысьте `protocol.Version`.
6. API и UI (ниже), тесты, документация.

### …эндпоинт админ-API

1. Метод в `server/api_mesh.go` (изменения) или `api_view.go` (чтение) на типе `*api`.
2. Регистрация в таблице `adminHandler` (`server/api.go`).
3. Ошибки — через `writeStoreError`; тело — через `decode` (он отвергает неизвестные поля).
4. После изменения вызовите `a.configChanged()`.
5. Клиент в `web/js/lib/api.js`, описание в `docs/en/API.md`, тест в `internal/server`.

### …страницу интерфейса

1. Файл в `web/js/pages/`, компонент `XxxPage`; данные — `useApp()` (общий снимок) или
   `usePolled(...)` (собственный опрос).
2. Добавьте её в список `PAGES` в `web/js/shell.js` (id, подпись, иконка).
3. Новые иконки — в `web/js/ui/icons.js`; общие элементы — в `web/js/ui/kit.js`.
4. Изменения данных — только через `mutate` или `useAction`.
5. Пересоберите бинарник, проверьте через стенд и `uishots.py`.

### …тип выходной точки

`protocol.Type*` → валидация в `store/model.go` (`Endpoint.normalize`) → исполнение в
`egress/egress.go` → поля в UI (`pages/egress.js`) → API-справочник.

## Выпуск

1. `gofmt -l .` пуст, `go vet ./...` чист, `CGO_ENABLED=1 go test -race ./...` зелёный.
2. `python3 scripts/uiflows.py` проходит на свежем стенде.
3. Обновите `protocol.AppVersion` (и `protocol.Version`/`schemaVersion` при несовместимых
   изменениях).
4. `scripts/release.sh`: собирает все цели и раскладывает в `dist/` файлы с именами
   `dyrokol-<ОС>-<архитектура>[.exe]` и `SHA256SUMS`. Именно эти имена ждёт `scripts/install.sh`.
5. Создайте тег (`git tag v1.0.0 && git push --tags`) и релиз на GitHub, приложите все файлы из `dist/`.
6. Проверьте, что `README.md` и `docs/ru`, `docs/en` соответствуют изменениям.

# ytr

Go SDK для Yandex Tracker: **199 HTTP-операций из 208 страниц официального справочника**. Документация проверена 31 октября 2026 года, revision `r21880163`.

Пакет `github.com/skarm/ytr/typed` предоставляет типизированные запросы и ответы всех операций. Корневой `github.com/skarm/ytr` предоставляет общий транспорт, декодирование в модели продукта и каталог для динамических оболочек. CLI `cmd/ytr` использует этот же транспорт. Требуется Go 1.27; сторонних runtime-зависимостей нет.

## Типизированный клиент

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/skarm/ytr"
    "github.com/skarm/ytr/typed"
)

func main() {
    tracker, err := typed.New(ytr.Config{
        Organization: ytr.Organization{Kind: ytr.Organization360, ID: os.Getenv("TRACKER_ORG_ID")},
        TokenSource: ytr.OAuthToken(os.Getenv("TRACKER_TOKEN")),
    })
    if err != nil { log.Fatal(err) }

    result, err := tracker.CreateIssue(context.Background(), typed.CreateIssueRequest{
        Notify: typed.Ptr(false),
        Body: typed.CreateIssueRequestBody{
            Summary: "Проверить интеграцию",
            Queue: typed.Ref("SDK"),
            Description: typed.Value("Текст описания"),
            Assignee: typed.Value(typed.IDRef("login")),
        },
        ExtraFields: ytr.Fields{"customField": "value"},
    })
    if err != nil { log.Fatal(err) }
    log.Println(result.Data.Key, result.StatusCode)
}
```

У каждого метода один request: параметры пути, query, заголовки и `Body` либо `File`. Например, `GetIssue(ctx, typed.GetIssueRequest{IssueID: "SDK-1", Fields: []string{"key", "summary"}})`. Опциональные query-параметры используют указатели; `typed.Ptr(false)` отправляет `false`, `nil` пропускает параметр. Списки и повторяемые параметры сериализуются согласно контракту операции.

JSON-поля `Optional[T]` различают пропуск, явный `null` и значение, включая `false`, ноль, пустую строку и пустой массив. Вложенные динамические значения generic-кодеков (`Optional`, `Update`, `OneOrMany`, `CollectionUpdate`) сохраняют числа как `json.Number`, включая целые больше 2^53. Для обновления:

```go
result, err := tracker.UpdateIssue(ctx, typed.UpdateIssueRequest{
    IssueID: "SDK-1",
    Body: typed.UpdateIssueRequestBody{
        Summary: typed.Value(typed.Replace("Новое название")),
        Description: typed.Null[typed.Update[string]](),
        Tags: typed.Value(typed.CollectionUpdate[string]{
            Add: typed.Value(typed.One("integration")),
            Remove: typed.Value(typed.Many([]string{"obsolete"})),
        }),
    },
})
```

`typed.Set(value)` задаёт оператор `set`, `typed.Collection(values)` — прямую замену массива. `CollectionUpdate[T]` также поддерживает `replace`: `typed.Value(typed.One(typed.Replacement[T]{Target: old, Replacement: new}))` для одного объекта или `typed.Value(typed.Many(replacements))` для массива. Типы операторов соответствуют полю; суммы вариантов представлены структурами с указателем на выбранный вариант. Одновременный выбор несовместимых вариантов возвращает ошибку до HTTP-запроса.

`Result[T]` содержит `Data`, HTTP-код и заголовки, включая пагинацию. При ошибке `Data` обнуляется; полученные метаданные сохраняются. Произвольные поля задачи доступны в `Issue.AdditionalFields` как `json.RawMessage`; пользовательские поля запроса задаются через `ExtraFields`. `UpdateIssueRequestBody.AdditionalFields` сохраняет дополнительные поля вложенных `values` и `issueUpdate`, включая явный `null`. Числа сохраняются как `json.Number`, целочисленные значения или явно документированный союз строкового и числового ID, без преобразования в `float64`.

Там, где документация допускает произвольное содержимое или не описывает форму значения, используется `json.RawMessage` либо карта с типизированными значениями. Неописанные свойства не выдумываются. Три операции с HTTP 200 без определённого тела допускают пустой или JSON-ответ; ответы 204 представлены пустой структурой. Бинарные ответы возвращают `*ytr.Download`.

Для Yandex Cloud используйте `OrganizationCloud` и соответствующий `IAMToken` или OAuth. Организация определяет заголовок `X-Org-ID` / `X-Cloud-Org-ID`, авторизация задаётся отдельно. Обновление credentials принадлежит приложению и реализуется через конкурентно безопасный `TokenSource`.

## Модели продукта и общий транспорт

Для узкой модели можно декодировать ответ без сохраняемой raw-копии и объекта `Response`:

```go
raw := tracker.Raw() // либо ytr.New(config)
var issue struct {
    Key string `json:"key"`
    Summary string `json:"summary"`
}
meta, err := raw.CallInto(ctx, ytr.OpGetIssue, []string{"SDK-1"}, nil, &issue)
```

`CallInto` использует размер ответа как подсказку для конкретных моделей известной длины; остальные ответы декодируются ограниченным потоком с `UseNumber`. `json.Decoder` также имеет внутренний буфер — меньшие выделения памяти не гарантируются для всех размеров и типов.

`typed.FromClient(raw)` разделяет существующий клиент и пул соединений. Типизированные методы используют `CallInto` / `UploadInto`; HTTP-логика и лимиты остаются в одном месте. Полная модель SDK хранит больше данных, чем проекция продукта; выбирайте нужные поля через query `fields`.

Если требуется исходное JSON-тело, именованные методы корневого клиента возвращают `*ytr.Response`:

```go
response, err := raw.GetIssue(ctx, "SDK-1")
if err != nil { return err }
if err := response.Decode(&issue); err != nil { return err }
```

`ytr.Decode[T](response, err)` принимает пару непосредственно из вызова и возвращает нулевое значение при ошибке. Корневые методы принимают структуры продукта, `ytr.Fields` или `json.RawMessage`. `Response.Decode` сохраняет `json.Number` для динамических значений. Ненужное повторное декодирование можно исключить через `CallInto`.

[examples/integration](examples/integration) — самостоятельный модуль: корневой пакет определяет модель `Issue` и порт `IssueClient` без зависимости от SDK; пакет `tracker` реализует этот порт через типизированный клиент. Тесты используют публичные импорты. Зависимости от rumo нет. Версии объектов SDK не определяет автоматически: передавайте документированный `Version` или `IfMatch`; сервер проверяет права и обязательные поля.

В примерах ниже `tracker` — корневой клиент, полученный через `typedClient.Raw()` или `ytr.New(config)`.

## Пагинация

```go
err := tracker.Paginate(ctx, ytr.OpSearchIssues, nil,
    ytr.Fields{"queue": "SDK"},
    func(page *ytr.Response) error {
        var issues []struct { Key string `json:"key"` }
        if err := page.Decode(&issues); err != nil { return err }
        for _, issue := range issues { log.Println(issue.Key) }
        return nil // ytr.ErrStopPagination завершает обход успешно
    }, ytr.Query("perPage", "100"))
```

`Paginate` следует `Link: rel=next` либо `X-Total-Pages`, сохраняет HTTP-метод и тело запроса и проверяет повтор страниц. Ссылки должны вести на тот же origin и ресурс. Для операций с ID передайте `[]string` в порядке параметров пути из каталога. Options копируются при создании и могут использоваться несколькими goroutines.

Для ответов с cursor внутри тела управляйте обходом явно через соответствующие методы: `ListUsersRelative`, `ListEntityCommentsRelative`, `GetEntityEvents` и другие. `Response.NextLink()` возвращает ссылку без выполнения запроса. Scroll доступен через параметры `SearchIssues` и отдельный `ClearIssueScroll`; последний принимает документированный объект `scrollID: scrollToken`. SDK не закрывает чужие scroll-сессии и не хранит их состояние.

## Файлы

```go
uploaded, err := tracker.UploadTemporaryAttachment(ctx, ytr.File{
    Name: "report.pdf", Reader: file,
})

download, err := tracker.DownloadIssueAttachment(ctx, "SDK-1", "123", "report.pdf")
if err != nil { return err }
defer download.Body.Close()
_, err = io.Copy(destination, download.Body)
```

Загрузка не буферизует содержимое файла и не закрывает reader приложения. Multipart-поле по умолчанию — `file`; его можно явно задать через `File.FieldName`. Для импорта также передайте документированные `filename`, `createdAt`, `createdBy` через query options. Типизированные multipart requests содержат `File` и query-параметры; JSON `Body` и `ExtraFields` у них отсутствуют. Скачивание возвращает поток, который необходимо закрыть.

## Ошибки и HTTP

`errors.As(err, &apiError)` извлекает `*ytr.APIError`: HTTP-код, заголовки, `errorMessages`, произвольные значения `errors`, исходное тело, признак усечения и причину ошибки чтения через `errors.Is`. `Error()` не включает данные сервера и токены. Для `429` доступен `RetryAfter(now)`; библиотека не повторяет запросы автоматически.

`context.Context` передается во все операции. По умолчанию HTTP timeout — 30 секунд; можно передать собственный `http.Client`. Клиент копируется, Transport остается общим. Автоматические redirects запрещены; ответ 3xx возвращается как APIError с доступным Location. Это предотвращает передачу организационных credentials на другой origin.

## Подключение к rumo локально

```sh
go mod edit -require=github.com/skarm/ytr@v0.0.0
go mod edit -replace=github.com/skarm/ytr=/Users/skarm/Projects/skarm/ytr
go mod tidy
```

Команды выполняются в модуле-потребителе. В этой работе rumo не изменяется.

## Публичный API для оболочек

CLI, HTTP adapters и другие динамические оболочки могут вызывать операции без reflection и доступа к `internal`:

```go
operation, err := ytr.LookupOperation(ytr.OpGetIssue)
// operation.PathParameters() == []string{"issueID"}

response, err := tracker.Call(ctx, operation.Name, []string{"SDK-1"}, nil)
```

`Call` принимает JSON или пустое тело, `Upload` — `File`, `Download` возвращает поток. Порядок аргументов пути доступен через `Operation.PathParameters()`. Неверный исполнитель возвращает `ErrOperationKind` до HTTP-запроса, неизвестное имя — `ErrUnknownOperation`. Именованные методы разделяют с ними HTTP-исполнители; повторный поиск операции им не требуется. `Operations()` возвращает каталог; его JSON-поля имеют имена `name`, `method`, `path`, `request`, `response`, `documentation`.

## CLI

Установить команду из локального checkout:

```sh
go install ./cmd/ytr
```

Credentials читает только CLI:

```sh
export YTR_TOKEN='...'
export YTR_ORG_ID='...'
export YTR_ORG_TYPE='360'       # либо cloud
export YTR_AUTH_SCHEME='OAuth' # либо Bearer для IAM
```

Примеры:

```sh
ytr operations
ytr help GetIssue
ytr GetIssue SDK-1
ytr -data @issue.json -query notify=false CreateIssue
ytr -all -data '{"queue":"SDK"}' SearchIssues
ytr -file ./report.pdf UploadIssueAttachment SDK-1
ytr DownloadIssueAttachment SDK-1 123 report.pdf > downloaded-report.pdf
```

Flags указываются перед именем операции. `-query name=value` и `-header 'Name: value'` можно повторять. JSON передается текстом, через `@file` или `-data -` из stdin. Для multipart `-file -` читает stdin потоково и требует `-filename`. `-field` меняет имя multipart-поля; query `filename` по умолчанию берется из имени файла и может быть переопределен через `-query`.

Обычный результат — один компактный JSON-документ в stdout; `-all` использует пагинацию пакета и выводит по одной JSON-странице на строку. Скачать файл можно напрямую в stdout без промежуточной буферизации. Ошибки идут в stderr. Exit codes: `0` — успех, `2` — некорректный вызов или конфигурация, `1` — ошибка API, транспорта или IO. `-timeout` ограничивает запрос/обход страниц, `-base-url` задает origin API. CLI использует тот же SDK-клиент и обработку context cancellation.

Таймаут CLI охватывает исполнение команды, включая чтение stdin и запись stdout. При таймауте или отмене команда возвращает ошибку, даже если чтение stdin или запись stdout заблокированы. CLI также пытается закрыть эти потоки; завершение команды не требует, чтобы закрытие прервало системный вызов. Успешная операция потоки не закрывает. Подробнее о жизненном цикле — в [описании архитектуры](docs/architecture.md#потребители).

## Проверки и обновление справочника

```sh
go test -race ./...
go -C examples/integration test -race ./...
go vet ./...
golangci-lint run --config .golangci.yml ./...
go generate ./...
go run ./cmd/checkdocs
go run ./cmd/checkdocs -online
```

Обычные тесты используют только in-memory HTTP fixtures: без реальных организаций, токенов и запросов к Tracker. Они вызывают каждый из 199 методов и сравнивают запрос с независимо сохраненной HTTP-сигнатурой документации. Генерация воспроизводима и не требует сети.

`api/documentation.json` содержит отдельно извлечённые из официальных страниц факты: таблицы параметров, отпечатки полных строк и неизменённые валидные JSON-примеры. Файл не строится из `api/models.json`. Offline-проверка связывает его с проверенными хешами источников; тесты вызывают SDK с опубликованными примерами. Число операций и revision в fixtures сверяются с каталогом, поэтому обновление не требует править числовые константы тестов.

Обновление контракта выполняется так:

```sh
go run ./cmd/checkdocs -online -report /tmp/ytr-review
# Прочитать /tmp/ytr-review/report.txt и report.json.
# Исправить модели/привязки и независимые fixtures по указанным источникам.
go run ./cmd/checkdocs -accept /tmp/ytr-review
```

При изменениях первая команда завершается с ошибкой и сохраняет отчёт: добавленные/удалённые страницы и поля, прежний и новый тип/обязательность, секцию и строку источника, DTO, операции и JSON pointers в `api/models.json`. Для однозначного изменения скалярного типа предлагается конкретный Go-тип с сохранением `Optional`/`Update`. Подсказки не применяются автоматически. При структурном редактировании реестра ориентируйтесь также на имена DTO и полей: индексы JSON pointers относятся к моменту отчёта. Неоднозначные таблицы и несовпадения таблиц с примерами не превращаются в выдуманные контракты.

После исправления `api/models.json` обновите ожидания `typed/testdata/requests.json` и `responses.json` непосредственно по документации. При необходимости измените `api/catalog.json`, вызывающий Go-код и классификацию новых страниц в `api/sources.json`. Не формируйте ожидаемые fixtures из исправленной схемы: тогда одна ошибка попадёт и в код, и в ожидание.

`-accept` проверяет исходную версию отчёта, создаёт отдельный каталог с кандидатом, повторно получает официальные страницы с закреплённой revision и сверяет извлечённые факты. Подмена фактов при сохранённых хешах страниц отклоняется. Затем команда синхронизирует revision и выполняет `go generate ./...`, `go test ./...`, `go vet ./...` и тесты модуля интеграции. Только успешный кандидат заменяет локальные реестры, снимок источников и bindings. Ошибка проверки сохраняет исходники; отмена до установки и изменения любых скопированных входов сборки и тестов блокируют замену. При сбое установки откат восстанавливает записанные файлы, сохраняя обнаруженные параллельные правки пользователя. Устаревший отчёт нужно получить заново. `-online` без `-report` сохраняет результат в `api-review/`.

Перед сборкой `-accept` заново сопоставляет изменённые строки с **исправленными** моделями. Неустранённый конфликт типа, новое поле без владельца и неизвестный или повреждённый контекст блокируют принятие. Старая подсказка в отчёте не считается выполненной правкой. Для страниц с несколькими операциями `operations[].sections` в `api/models.json` явно перечисляет anchors разделов, например `["all-launches"]` для `ListAutoactionLogs` и `["autoaction-launch"]` для `GetAutoactionLog`. Общие разделы перечисляются у обеих операций; исчезнувший anchor отклоняется.

Если источник противоречит опубликованному примеру или не определяет контракт, решение записывается в `pages[].field_decisions` нужной страницы `api/sources.json`:

```json
{
  "section": "<after.section из report.json>",
  "digest": "<after.digest из report.json>",
  "model": "CreateIssueRequestBody",
  "field": "Unique",
  "type": "Optional[string]",
  "reason": "<обоснование сохранённого типа со ссылкой на проверенный контекст>"
}
```

Решение действует только для этой строки, секции, модели, поля и текущего типа. Для строки без сопоставления `model`, `field`, `type` отсутствуют; `section`, `digest`, `reason` обязательны. Общее решение для страницы не разрешает известное расхождение конкретного DTO.

Поля полностью новой страницы проходят ту же проверку, что и изменения существующих строк. Для удалённого поля оставшаяся привязка DTO требует отдельного решения с `before.section` и `before.digest` из отчёта; без него устаревшая привязка блокирует принятие.

Каждая HTTP-сигнатура source snapshot должна быть операцией SDK либо иметь явную классификацию в `pages[].excluded_signatures`: `signature`, `digest` всей страницы из `candidate.json`, `reason`. Конкретные примеры, ссылки на другие методы, упоминание устаревшего `POST /v3/boards` и противоречивый пример создания версии очереди перечислены отдельно. Ссылка на `GET /v3/gaps/workflows` классифицирована как вспомогательная: эти страницы описывают отсутствие, но не контракт ответа списка типов отсутствий. Новая неклассифицированная сигнатура блокирует принятие. При изменении страницы её классификации нужно сверить и явно обновить digest; команда не переносит решения на новый текст автоматически.

Официальная страница `api/admin/patch-issue-type.md` показывает и объект, и массив объектов в PATCH-примерах, хотя таблица описывает один объект. `UpdateIssueTypeRequestBody` сохраняет обе опубликованные формы через варианты `Object` и `Array`; выбор формы остаётся явным. Ошибки разметки таблиц записываются как примечания к фактам источника.

CI проверяет оба Go-модуля, race, vet и отсутствие изменений после генерации. Онлайн-сверка запускается вручную через `workflow_dispatch` с `online=true` и сохраняет отчёт как artifact. Обычные CI-проверки не обращаются к Tracker или документации.

Для обновления операций измените `api/catalog.json`, для DTO и привязок path/query/header/body/response — `api/models.json`. В моделях хранятся поля, типы, варианты, правила присутствия и ссылки на источники. Связанные страницы и независимые HTTP-сигнатуры находятся в `api/sources.json`, снимок индекса — в `api/index.txt`.

`go generate ./...` воспроизводит `methods_gen.go`, `typed/models_gen.go` и `typed/client_gen.go`. Генератор использует общий с `checkdocs` валидатор `internal/apidoc`: проверяет имена и ссылки типов, уникальность JSON-полей, присутствие `Optional`, union/discriminator, циклы значений и полную привязку request-полей к path/query/header/body/file. Каталог загружается один раз; revision, состав операций, encoding, источники и path-параметры должны совпадать. Типы в реестре записываются компактно, например `Optional[Update[string]]`; alias допускает скаляр, срез или map, чтобы не терять пользовательские кодеки при создании нового Go-типа. Все три Go-файла формируются до начала записи: ошибка входных данных или шаблона сохраняет существующие bindings. Сгенерированные файлы включены в пакет; потребителю генерация не нужна. При выполнении запросов JSON-реестры и документация не читаются.

Валидация каталога отклоняет конфликты имён методов и параметров с кодом SDK, повреждённые placeholders, query/fragment внутри пути и неподдерживаемое сочетание тела со streaming-ответом до записи bindings. Unicode-имена параметров преобразуются по целым символам.

Тест генератора компилирует и исполняет небольшой клиент с вручную заданными HTTP-ожиданиями; намеренная замена `query.Add` на `query.Set` должна ломать эти ожидания. Дополнительные mutation-тесты проверяют отклонение испорченных схем; fuzz-тесты покрывают обработку некорректных входов.

Независимые fixtures в `typed/testdata` проверяют все операции, известные поля DTO и опубликованные JSON-примеры; при изменении контрактов обновляйте их после сверки документации. Бенчмарки используют локальный transport и не измеряют производительность сервера Tracker. Методика, ограничения и результаты измерений приведены в [docs/performance.md](docs/performance.md).

[Устройство пакета и карта исходников](docs/architecture.md).

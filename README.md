# ytr

Go SDK для Yandex Tracker с типизированным клиентом и CLI. Требуется Go 1.27 или новее; сторонних зависимостей нет.

SDK покрывает 199 HTTP-операций из 208 страниц сохранённого справочника. Снимок проверен 31 октября 2026 года, revision `r21880163`; последующие изменения API нужно сверять отдельно.

## Установка

Добавьте SDK в модуль приложения:

```sh
go get github.com/skarm/ytr
```

Пакет `github.com/skarm/ytr/typed` содержит типизированные запросы и ответы. Корневой пакет `github.com/skarm/ytr` отвечает за авторизацию, HTTP-транспорт и работу с собственными моделями.

## Создание задачи

Передайте ID организации и OAuth-токен через переменные окружения `YTR_ORG_ID` и `YTR_TOKEN`:

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
		Organization: ytr.Organization{
			Kind: ytr.Organization360,
			ID:   os.Getenv("YTR_ORG_ID"),
		},
		TokenSource: ytr.OAuthToken(os.Getenv("YTR_TOKEN")),
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := tracker.CreateIssue(context.Background(), typed.CreateIssueRequest{
		Notify: typed.Ptr(false),
		Body: typed.CreateIssueRequestBody{
			Summary: "Проверить интеграцию",
			Queue:   typed.Ref("SDK"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(result.Data.Key, result.StatusCode)
}
```

Для Yandex Cloud укажите `ytr.OrganizationCloud` и подходящий `ytr.IAMToken` или OAuth-токен. Тип организации определяет заголовок `X-Org-ID` или `X-Cloud-Org-ID`. Обновление токенов реализует приложение через `ytr.TokenSource`.

Каждый типизированный метод принимает одну структуру запроса с параметрами пути, query, заголовками и телом. Для необязательных query-параметров используйте указатели: `typed.Ptr(false)` отправляет `false`, а `nil` пропускает параметр.

## Обновление полей

JSON-поля `typed.Optional[T]` различают пропуск, явный `null` и значение. Неинициализированное поле пропускается; `typed.Value(value)` отправляет значение, включая `false`, ноль или пустой массив; `typed.Null[T]()` отправляет `null`.

Для обновления задачи используйте типы операторов соответствующего поля:

```go
result, err := tracker.UpdateIssue(context.Background(), typed.UpdateIssueRequest{
	IssueID: "SDK-1",
	Body: typed.UpdateIssueRequestBody{
		Summary:     typed.Value(typed.Replace("Новое название")),
		Description: typed.Null[typed.Update[string]](),
		Tags: typed.Value(typed.CollectionUpdate[string]{
			Add:    typed.Value(typed.One("integration")),
			Remove: typed.Value(typed.Many([]string{"obsolete"})),
		}),
	},
})
```

`typed.Set(value)` задаёт оператор `set`, `typed.Collection(values)` заменяет массив целиком. Для коллекций также доступны `add`, `remove` и `replace`. Несовместимые варианты запроса возвращают ошибку до HTTP-вызова.

Пользовательские поля запроса передавайте через `ExtraFields`. Дополнительные поля ответа доступны в `AdditionalFields` как `json.RawMessage`. Динамические значения generic-кодеков сохраняют числа через `json.Number`, включая целые больше 2^53.

## Собственные модели

Получите корневой клиент через `tracker.Raw()` или создайте его с помощью `ytr.New`. `CallInto` декодирует ответ в модель приложения. В примерах далее `ctx` обозначает контекст вызова:

```go
raw := tracker.Raw()
var issue struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
}
meta, err := raw.CallInto(ctx, ytr.OpGetIssue, []string{"SDK-1"}, nil, &issue)
```

Если нужно исходное JSON-тело, используйте именованный метод корневого клиента, например `raw.GetIssue`, и `Response.Decode`. Функция `typed.FromClient(raw)` создаёт типизированный клиент поверх существующего транспорта.

В [примере интеграции](examples/integration) модель продукта и интерфейс `IssueClient` отделены от адаптера SDK. Для динамического вызова доступны `LookupOperation`, `Operations`, `Call`, `Upload` и `Download`; порядок параметров пути задаёт `Operation.PathParameters()`.

## Пагинация и файлы

`Paginate` обходит страницы по `Link: rel=next` или `X-Total-Pages`, сохраняя метод и тело запроса. Ссылки должны вести на тот же origin и ресурс. Обход можно остановить возвратом `ytr.ErrStopPagination`:

```go
err := tracker.Raw().Paginate(ctx, ytr.OpSearchIssues, nil,
	ytr.Fields{"queue": "SDK"},
	func(page *ytr.Response) error {
		var issues []struct{ Key string `json:"key"` }
		if err := page.Decode(&issues); err != nil {
			return err
		}
		for _, issue := range issues {
			log.Println(issue.Key)
		}
		return nil
	}, ytr.Query("perPage", "100"))
```

Ответы с курсором внутри тела и scroll-сессии обрабатывайте явно через соответствующие методы API. SDK не закрывает scroll-сессии автоматически.

Для загрузки передайте `ytr.File` с именем и `io.Reader`. SDK читает файл потоково и не закрывает переданный reader. Скачивание возвращает `*ytr.Download`; после чтения закройте `Body`.

## Ошибки и HTTP

`typed.Result[T]` содержит `Data`, `StatusCode` и `Headers`. При ошибке `Data` обнуляется, а полученные метаданные сохраняются. `errors.As` позволяет извлечь `*ytr.APIError` с HTTP-кодом, заголовками и телом ошибки. Для ответа `429` доступен `RetryAfter(now)`; автоматических повторов нет.

Все операции принимают `context.Context`. По умолчанию HTTP-таймаут составляет 30 секунд, лимит JSON-ответа составляет 16 MiB. Их можно настроить через `ytr.Config`. Клиент запрещает автоматические перенаправления и возвращает ответы 3xx как ошибки API.

Клиент можно использовать из нескольких goroutines, если переданные `TokenSource` и HTTP-транспорт также поддерживают конкурентный доступ. Не меняйте тела запросов и файлы во время вызова. Версию объекта для `Version` или `IfMatch` передаёт приложение.

## CLI

Установите CLI из корня репозитория:

```sh
go install ./cmd/ytr
```

Для запросов задайте организацию и токен:

```sh
export YTR_TOKEN='your_access_token_here'
export YTR_ORG_ID='your_organization_id_here'
export YTR_ORG_TYPE='360'       # либо cloud
export YTR_AUTH_SCHEME='OAuth' # либо Bearer для IAM
```

Список операций и справка доступны без авторизации. Флаги указывайте перед именем операции:

```sh
ytr operations
ytr help GetIssue
ytr GetIssue SDK-1
ytr -data @issue.json -query notify=false CreateIssue
ytr -all -data '{"queue":"SDK"}' SearchIssues
ytr -file ./report.pdf UploadIssueAttachment SDK-1
ytr DownloadIssueAttachment SDK-1 123 report.pdf > downloaded-report.pdf
```

`-query name=value` и `-header 'Name: value'` можно повторять. `-data` принимает JSON, `@file` или `-` для stdin. Для загрузки из stdin укажите `-file - -filename report.pdf`; `-field` меняет имя multipart-поля.

CLI выводит JSON в stdout, а с `-all` выводит по одной странице на строку. Скачиваемый файл передаётся в stdout потоком. Ошибки идут в stderr. Коды завершения: `0` при успехе, `2` при ошибке вызова или конфигурации, `1` при ошибке API, транспорта или ввода-вывода.

`-timeout` ограничивает всю команду, включая обход страниц и ввод-вывод. `-base-url` задаёт origin API. При отмене или таймауте CLI возвращает ошибку и пытается закрыть потоки; успешная операция их не закрывает.

## Разработка

Команды проверки, устройство реестров API и порядок обновления контрактов описаны в [CONTRIBUTING.md](CONTRIBUTING.md).

# shopify-client-golang

一个 Shopify Admin API GraphQL 客户端，支持：

- 固定 token 鉴权
- Client Credentials 动态鉴权（自动缓存与刷新 access token）
- GraphQL 请求重试（含限流场景处理）

## 安装

```bash
go get github.com/lascyb/shopify-client-golang
```

## 快速开始

### 1) 固定 token 鉴权

```go
package main

import (
	"context"
	"fmt"

	shopify "github.com/lascyb/shopify-client-golang"
)

type QueryResult struct {
	Shop struct {
		Name string `json:"name"`
	} `json:"shop"`
}

func main() {
	auth := shopify.NewAuthWithStatic("your-store.myshopify.com", "shpat_xxx")

	client, err := shopify.NewClient(auth)
	if err != nil {
		panic(err)
	}

	query := `query { shop { name } }`
	var result QueryResult
	if err = client.GraphQLWithRetry(context.Background(), query, nil, &result); err != nil {
		panic(err)
	}

	fmt.Println(result.Shop.Name)
}
```

### 2) Client Credentials 动态鉴权

```go
package main

import (
	"context"
	"fmt"

	shopify "github.com/lascyb/shopify-client-golang"
	"github.com/lascyb/shopify-client-golang/options"
)

type QueryResult struct {
	Shop struct {
		Name string `json:"name"`
	} `json:"shop"`
}

func main() {
	auth := shopify.NewAuth("your-store.myshopify.com", "client_id", "client_secret")

	client, err := shopify.NewClient(
		auth,
		options.WithApiVersion("2026-04"),
		options.WithMaxRetry(5),
	)
	if err != nil {
		panic(err)
	}

	query := `query { shop { name } }`
	var result QueryResult
	if err = client.GraphQLWithRetry(context.Background(), query, nil, &result); err != nil {
		panic(err)
	}

	fmt.Println(result.Shop.Name)
}
```

## API 说明

### 鉴权

- `shopify.NewAuthWithStatic(domain, accessToken)`：固定 token。
- `shopify.NewAuth(domain, clientID, clientSecret)`：动态 token（缓存并在过期前刷新）。

### 客户端

- `shopify.NewClient(auth, opt...)`：创建客户端。
- `(*Client).GraphQL(ctx, query, variables)`：单次请求，不做整体重试。
- `(*Client).GraphQLWithRetry(ctx, query, variables, result)`：带重试请求并将 `data` 反序列化到 `result`。

### 可选项（options）

- `options.WithApiVersion(version)`：设置 Admin API 版本。
- `options.WithHttpClient(httpClient)`：自定义 HTTP 客户端。
- `options.WithProxy(proxyURL)`：设置代理。
- `options.WithLogger(logger)`：设置日志器。
- `options.WithMaxRetry(n)`：设置 `GraphQLWithRetry` 最大重试次数。

## 错误与重试行为

- HTTP `5xx` / `408` / `429` 会被判定为可重试错误。
- 网络错误会被包装为可重试错误。
- GraphQL 限流错误（如 `THROTTLED`）会自动等待后重试。
- 鉴权错误、JSON 解析错误通常不可重试。

## 注意事项

- `GraphQLWithRetry` 要求 `result` 为可写入的结构体指针。
- 建议显式设置 `WithApiVersion`，避免依赖默认版本值。

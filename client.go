// Package shopify 提供 Shopify Admin API（GraphQL）的轻量客户端。
package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lascyb/shopify-client-golang/auth"
	"github.com/lascyb/shopify-client-golang/common"
	"github.com/lascyb/shopify-client-golang/options"
)

// maxGraphQLThrottleRetries GraphQL 响应为 THROTTLED 时，GraphQLWithRetry 内层等待并重试的最大次数
const maxGraphQLThrottleRetries = 12

// Auth 用于获取当前可用的 Shopify Admin API access token。
type Auth interface {
	AccessToken(ctx context.Context) (string, error)
	Domain() string
}

// NewAuthWithStatic 使用固定 Admin API access token 创建 Auth（旧式固定 token）。
func NewAuthWithStatic(domain, accessToken string) Auth {
	return auth.NewStaticAuth(domain, accessToken)
}

// NewAuth 使用 client credentials grant 动态获取 access token 创建 Auth（新式应用）。
func NewAuth(domain, clientID, clientSecret string) Auth {
	return auth.NewClientCredentialsAuth(domain, clientID, clientSecret)
}

// Client 封装对 https://{shopDomain}/admin/api/{version}/graphql.json 的请求。
// 一个 Client 只绑定一个 shopDomain。
type Client struct {
	auth            Auth
	domain          string
	apiVersion      string
	httpClient      *http.Client
	logger          *slog.Logger
	MaxRetry        int // GraphQLWithRetry 使用；<=0 时按默认 5
	graphQLEndpoint string
}

type GraphQLRequest struct {
	Query     string `json:"query"`
	Variables any    `json:"variables,omitempty"`
}

type GraphQLError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

type GraphQLResponse struct {
	Data       json.RawMessage `json:"data,omitempty"`
	Errors     []GraphQLError  `json:"errors,omitempty"`
	Extensions map[string]any  `json:"extensions,omitempty"`
}

// NewClient 创建 Admin GraphQL 客户端。
func NewClient(auth Auth, opt ...options.Option) (*Client, error) {
	if auth == nil {
		return nil, fmt.Errorf("shopify: auth 不能为空")
	}
	if auth.Domain() == "" {
		return nil, fmt.Errorf("shopify: shopDomain 不能为空")
	}

	cfg := options.NewConfig(opt...)

	cli := &Client{
		domain:          auth.Domain(),
		apiVersion:      cfg.APIVersion,
		httpClient:      cfg.HTTPClient,
		auth:            auth,
		MaxRetry:        cfg.MaxRetry,
		graphQLEndpoint: fmt.Sprintf("https://%s/admin/api/%s/graphql.json", auth.Domain(), cfg.APIVersion),
	}
	if cfg.Logger == nil {
		cli.logger = slog.With(slog.String("domain", auth.Domain()))
	} else {
		cli.logger = cfg.Logger
	}
	if cli.MaxRetry <= 0 {
		cli.MaxRetry = 5
	}

	return cli, nil
}

// GraphQL 单次 GraphQL POST：一次 HTTP、附带 token、解析 JSON 为 GraphQLResponse；不处理 THROTTLED、不解释 errors 业务语义。
// HTTP 200 且 JSON 能解出 GraphQLResponse 时一律返回 error==nil，即使 len(resp.Errors)>0。
func (c *Client) GraphQL(ctx context.Context, query string, variables any) (*GraphQLResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("shopify: client 未初始化")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("shopify: query 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	token, err := c.auth.AccessToken(ctx)
	if err != nil {
		return nil, &common.ErrAuth{Err: fmt.Errorf("shopify: 获取 access token 失败：%w", err)}
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &common.ErrAuth{Err: fmt.Errorf("shopify: access token 为空")}
	}

	reqBody, err := json.Marshal(GraphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, &common.ErrGraphQLPermanent{Err: fmt.Errorf("shopify: 编码请求体失败：%w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, &common.ErrGraphQLPermanent{Err: fmt.Errorf("shopify: 构建请求失败：%w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, common.WrapNetworkError(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, common.WrapNetworkError(fmt.Errorf("shopify: 读取响应失败：%w", err))
	}

	if resp.StatusCode != http.StatusOK {
		body := string(respBytes)
		if common.IsTransientHTTPStatus(resp.StatusCode) {
			return nil, &common.ErrTransientHTTP{StatusCode: resp.StatusCode, Body: body}
		}
		return nil, &common.ErrPermanentHTTP{StatusCode: resp.StatusCode, Body: body}
	}

	var gql GraphQLResponse
	if err = json.Unmarshal(respBytes, &gql); err != nil {
		return nil, &common.ErrJSONDecode{Err: fmt.Errorf("shopify: 解析响应失败：%w", err)}
	}
	return &gql, nil
}

// GraphQLWithRetry 在 GraphQL 单次请求之上做 THROTTLED 等待、GraphQL errors 策略与指数退避重试，并将 data 反序列化到 result。
func (c *Client) GraphQLWithRetry(ctx context.Context, query string, variables any, result any) error {
	if c == nil {
		return fmt.Errorf("shopify: client 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := uuid.New().String()
	queryStartTime := time.Now()
	maxRetry := c.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 5
	}
	shopLabel := c.domain

	errMses := make([]string, 0)
	for retryCount := 0; retryCount <= maxRetry; retryCount++ {
		attemptStartTime := time.Now()
		if retryCount > 0 {
			delaySeconds := float64(uint(1)<<uint(retryCount-1)) + rand.Float64()*0.5
			if delaySeconds > 30 {
				delaySeconds = 30
			}
			c.logger.Warn("GraphQL 查询重试", "request_id", requestID, "retry", retryCount, "max_retry", maxRetry, "delay_sec", delaySeconds)
			select {
			case <-time.After(time.Duration(delaySeconds * float64(time.Second))):
			case <-ctx.Done():
				return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 重试退避取消: %w", requestID, shopLabel, ctx.Err())
			}
		}

		var resp *GraphQLResponse
		var lastErr error
		for throttleRound := 0; ; throttleRound++ {
			var err error
			resp, err = c.GraphQL(ctx, query, variables)
			if err != nil {
				lastErr = err
				break
			}
			if len(resp.Errors) == 0 {
				lastErr = nil
				break
			}
			if graphQLIsThrottled(resp.Errors) && throttleRound < maxGraphQLThrottleRetries {
				wait := graphQLThrottleWaitDuration(resp.Errors)
				c.logger.Warn("GraphQL THROTTLED，等待后自动重试",
					"request_id", requestID,
					"wait", wait.Round(time.Millisecond),
					"round", throttleRound+1,
					"max_rounds", maxGraphQLThrottleRetries)
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 限流等待取消: %w", requestID, shopLabel, ctx.Err())
				}
				continue
			}
			lastErr = c.applyGraphQLErrorsPolicy(resp)
			break
		}
		if lastErr != nil {
			errMses = append(errMses, fmt.Sprintf("[API请求:%s] 店铺[%s]第%d/%d次 GraphQL查询失败: %v，本次耗时: %.2f秒", requestID, shopLabel, retryCount, maxRetry, lastErr, time.Since(attemptStartTime).Seconds()))
			hasPartialData := resp != nil && graphQLResponseHasData(resp.Data)
			if hasPartialData {
				if err := json.Unmarshal(resp.Data, result); err != nil {
					c.logger.Debug("GraphQL 部分成功数据解析失败", "body", string(resp.Data), "err", err)
					decodeErr := &common.ErrJSONDecode{Err: err}
					return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 部分成功数据解析失败（原错误: %v）: %w", requestID, shopLabel, lastErr, decodeErr)
				}
			} else {
				body := ""
				if resp != nil {
					body = string(resp.Data)
				}
				c.logger.Debug("GraphQL 未返回部分成功数据", "resp_nil", resp == nil, "body", body)
			}
			if !common.GraphQLShouldRetry(lastErr) {
				if hasPartialData {
					return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 查询部分成功（数据已回填）但仍有错误: %w", requestID, shopLabel, lastErr)
				}
				return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 查询失败（不可重试）: %w", requestID, shopLabel, lastErr)
			}
			if retryCount >= maxRetry {
				if hasPartialData {
					return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 重试达到上限（数据已回填）但仍有错误: %w", requestID, shopLabel, lastErr)
				}
				break
			}
			continue
		}

		if resp == nil {
			return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 响应为空", requestID, shopLabel)
		}
		if err := json.Unmarshal(resp.Data, result); err != nil {
			c.logger.Debug("GraphQL 响应 JSON 解析失败，原始 data", "body", string(resp.Data), "err", err)
			decodeErr := &common.ErrJSONDecode{Err: err}
			return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL 响应解析失败（不可重试）: %w", requestID, shopLabel, decodeErr)
		}
		if retryCount > 0 {
			c.logger.Warn("GraphQL请求经重试后完成", "request_id", requestID, "retries", retryCount, "total_sec", time.Since(queryStartTime).String())
		} else {
			c.logger.Info("GraphQL请求完成", "request_id", requestID, "total_sec", time.Since(queryStartTime).Seconds())
		}
		return nil
	}
	return fmt.Errorf("[API请求:%s] 店铺[%s] GraphQL查询最终失败，已重试%d次，总耗时: %s: %s", requestID, shopLabel, maxRetry, time.Since(queryStartTime).String(), strings.Join(errMses, ";"))
}

// applyGraphQLErrorsPolicy 处理 GraphQL errors（部分成功、可重试的限流类 errors 等），供 GraphQLWithRetry 调用。
func (c *Client) applyGraphQLErrorsPolicy(gql *GraphQLResponse) error {
	if len(gql.Errors) == 0 {
		return nil
	}
	marshal, mErr := json.Marshal(gql.Errors)
	if mErr != nil {
		return &common.ErrGraphQLPermanent{Err: mErr}
	}
	errStr := string(marshal)
	if graphQLErrorsShouldRetry(gql.Errors) {
		return &common.ErrGraphQLRetryable{Err: fmt.Errorf("shopify: GraphQL 错误 errors=%s", errStr)}
	}
	if graphQLResponseHasData(gql.Data) {
		c.logger.Warn("GraphQL 部分成功但响应含 errors", "errors", errStr)
		return &common.ErrGraphQLPermanent{Err: fmt.Errorf("GraphQL 部分成功但响应含 errors:%s", errStr)}
	}
	return &common.ErrGraphQLPermanent{Err: fmt.Errorf("shopify: GraphQL 错误 errors=%s", errStr)}
}

// graphQLIsThrottled 判断是否为 Admin API 明确限流（THROTTLED / Rate limited）
func graphQLIsThrottled(errors []GraphQLError) bool {
	for _, e := range errors {
		if code, ok := e.Extensions["code"].(string); ok {
			if strings.ToUpper(strings.TrimSpace(code)) == "THROTTLED" {
				return true
			}
		}
		if strings.Contains(strings.ToLower(e.Message), "rate limited") {
			return true
		}
	}
	return false
}

// graphQLThrottleWaitDuration 从 THROTTLED 的 extensions.cost.windowResetAt 计算等待时长；无则默认短等待
func graphQLThrottleWaitDuration(errors []GraphQLError) time.Duration {
	const (
		defaultWait = 2 * time.Second
		minWait     = 500 * time.Millisecond
		maxWait     = 90 * time.Second
		padding     = time.Second
	)
	for _, e := range errors {
		if code, ok := e.Extensions["code"].(string); !ok || strings.ToUpper(strings.TrimSpace(code)) != "THROTTLED" {
			if strings.Contains(strings.ToLower(e.Message), "rate limited") {
				return defaultWait
			}
			continue
		}
		cost, ok := e.Extensions["cost"].(map[string]any)
		if !ok {
			return defaultWait
		}
		raw, _ := cost["windowResetAt"].(string)
		if strings.TrimSpace(raw) == "" {
			return defaultWait
		}
		resetAt, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			resetAt, err = time.Parse(time.RFC3339Nano, raw)
		}
		if err != nil {
			return defaultWait
		}
		d := time.Until(resetAt) + padding
		if d < minWait {
			return minWait
		}
		if d > maxWait {
			return maxWait
		}
		return d
	}
	return defaultWait
}

// graphQLErrorsShouldRetry 根据 GraphQL errors 判断是否应由上层重试（限流用尽内层重试后仍失败等）
func graphQLErrorsShouldRetry(errors []GraphQLError) bool {
	for _, e := range errors {
		if code, ok := e.Extensions["code"].(string); ok {
			switch strings.ToUpper(strings.TrimSpace(code)) {
			case "THROTTLED":
				return true
			}
		}
		if strings.Contains(strings.ToLower(e.Message), "rate limited") {
			return true
		}
	}
	return false
}

// graphQLResponseHasData 判断响应是否含可解析的 data（非空且非 JSON null），用于区分「仅 errors」与「data+errors 部分成功」
func graphQLResponseHasData(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	s := bytes.TrimSpace(data)
	return !bytes.Equal(s, []byte("null"))
}

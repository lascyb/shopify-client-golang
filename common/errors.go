// Package common 提供 Shopify SDK 侧共用类型（错误分类等）。
package common

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// GraphQLRetryPolicy GraphQLWithRetry 层用于判断是否值得再次发起请求。
type GraphQLRetryPolicy interface {
	GraphQLShouldRetry() bool
}

// ErrTransientHTTP HTTP 层瞬时错误（宜重试）：5xx、408、429。
type ErrTransientHTTP struct {
	StatusCode int
	Body       string
}

func (e *ErrTransientHTTP) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Body) == "" {
		return "shopify: HTTP 瞬时错误 status=" + strconv.Itoa(e.StatusCode)
	}
	return "shopify: HTTP 瞬时错误 status=" + strconv.Itoa(e.StatusCode) + " body=" + e.Body
}

func (e *ErrTransientHTTP) GraphQLShouldRetry() bool { return true }

// ErrPermanentHTTP HTTP 层不应重试：除 408/429 外的 4xx。
type ErrPermanentHTTP struct {
	StatusCode int
	Body       string
}

func (e *ErrPermanentHTTP) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Body) == "" {
		return "shopify: HTTP 客户端/权限类错误 status=" + strconv.Itoa(e.StatusCode)
	}
	return "shopify: HTTP 客户端/权限类错误 status=" + strconv.Itoa(e.StatusCode) + " body=" + e.Body
}

func (e *ErrPermanentHTTP) GraphQLShouldRetry() bool { return false }

// ErrNetwork 网络传输错误（宜重试）：连接失败、超时、临时性对端关闭等。
type ErrNetwork struct {
	Err error
}

func (e *ErrNetwork) Error() string {
	if e == nil || e.Err == nil {
		return "shopify: 网络错误"
	}
	return "shopify: 网络错误: " + e.Err.Error()
}

func (e *ErrNetwork) Unwrap() error { return e.Err }

func (e *ErrNetwork) GraphQLShouldRetry() bool { return true }

// ErrAuth 鉴权或 token 获取失败（不应重试）。
type ErrAuth struct {
	Err error
}

func (e *ErrAuth) Error() string {
	if e == nil || e.Err == nil {
		return "shopify: 鉴权失败"
	}
	return "shopify: 鉴权失败: " + e.Err.Error()
}

func (e *ErrAuth) Unwrap() error { return e.Err }

func (e *ErrAuth) GraphQLShouldRetry() bool { return false }

// ErrGraphQLRetryable GraphQL errors 中表示限流等、宜由上层再次发起整次请求的情况。
type ErrGraphQLRetryable struct {
	Err error
}

func (e *ErrGraphQLRetryable) Error() string {
	if e == nil || e.Err == nil {
		return "shopify: GraphQL 可重试错误"
	}
	return "shopify: GraphQL 可重试错误: " + e.Err.Error()
}

func (e *ErrGraphQLRetryable) Unwrap() error { return e.Err }

func (e *ErrGraphQLRetryable) GraphQLShouldRetry() bool { return true }

// ErrGraphQLPermanent GraphQL 用户/权限/入参类错误，或部分成功但含 errors，不应盲目重试同一请求。
type ErrGraphQLPermanent struct {
	Err error
}

func (e *ErrGraphQLPermanent) Error() string {
	if e == nil || e.Err == nil {
		return "shopify: GraphQL 不可重试错误"
	}
	return "shopify: GraphQL 不可重试错误: " + e.Err.Error()
}

func (e *ErrGraphQLPermanent) Unwrap() error { return e.Err }

func (e *ErrGraphQLPermanent) GraphQLShouldRetry() bool { return false }

// ErrJSONDecode 解析 GraphQL data JSON 失败（协议或字段不匹配），重试同一请求通常无效。
type ErrJSONDecode struct {
	Err error
}

func (e *ErrJSONDecode) Error() string {
	if e == nil || e.Err == nil {
		return "shopify: 响应 JSON 解析失败"
	}
	return "shopify: 响应 JSON 解析失败: " + e.Err.Error()
}

func (e *ErrJSONDecode) Unwrap() error { return e.Err }

func (e *ErrJSONDecode) GraphQLShouldRetry() bool { return false }

// GraphQLShouldRetry 判断 GraphQLWithRetry 是否应消耗重试次数再次请求。
func GraphQLShouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var p GraphQLRetryPolicy
	if errors.As(err, &p) {
		return p.GraphQLShouldRetry()
	}
	return false
}

// IsTransientHTTPStatus 判断 HTTP 状态码是否宜在 GraphQLWithRetry 层重试。
func IsTransientHTTPStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return code >= 500 && code <= 599
	}
}

// WrapNetworkError 将 *http.Client.Do 等错误包装为可重试或不可重试类型。
func WrapNetworkError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return &ErrNetwork{Err: err}
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return &ErrNetwork{Err: err}
	}
	// TLS、DNS 等仍可能为瞬时故障，保守视为可重试
	if strings.Contains(strings.ToLower(err.Error()), "connection refused") ||
		strings.Contains(strings.ToLower(err.Error()), "connection reset") ||
		strings.Contains(strings.ToLower(err.Error()), "broken pipe") ||
		strings.Contains(strings.ToLower(err.Error()), "eof") {
		return &ErrNetwork{Err: err}
	}
	return &ErrNetwork{Err: err}
}

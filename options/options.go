// Package options 提供 Shopify Admin GraphQL 客户端的配置选项（functional options）。
package options

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIVersion = "2026-04"

// Config 表示 Shopify Admin GraphQL 客户端的最终配置。
type Config struct {
	APIVersion string
	HTTPClient *http.Client
	Logger     *slog.Logger
	proxy      *url.URL
	MaxRetry   int // GraphQLWithRetry 最大重试次数，0 表示使用客户端默认值
}

// Option 用于修改 Config（functional options）。
type Option func(*Config)

// NewConfig 根据默认值并应用 opt，生成最终配置。
func NewConfig(opt ...Option) *Config {
	c := &Config{}
	for _, o := range opt {
		if o == nil {
			continue
		}
		o(c)
	}
	if c.HTTPClient == nil {
		if c.proxy != nil {
			c.HTTPClient = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(c.proxy),
				},
				Timeout: 5 * time.Minute,
			}
		} else {
			c.HTTPClient = &http.Client{
				Timeout: 5 * time.Minute,
			}
		}
	}
	if c.APIVersion == "" {
		c.APIVersion = defaultAPIVersion
	}
	return c
}

// WithApiVersion 设置 Shopify Admin API 版本。
func WithApiVersion(version string) Option {
	return func(c *Config) {
		if c == nil {
			return
		}
		c.APIVersion = strings.TrimSpace(version)
	}
}

// WithHttpClient 设置 HTTP 客户端（用于 GraphQL 请求与动态 token 获取）。
func WithHttpClient(httpClient *http.Client) Option {
	return func(c *Config) {
		if c == nil {
			return
		}
		c.HTTPClient = httpClient
	}
}

// WithProxy 设置 HTTP 客户端代理
func WithProxy(proxy *url.URL) Option {
	return func(c *Config) {
		if c == nil {
			return
		}
		c.proxy = proxy
	}
}

// WithLogger 设置日志容器
func WithLogger(logger *slog.Logger) Option {
	return func(c *Config) {
		if c == nil {
			return
		}
		c.Logger = logger
	}
}

// WithMaxRetry 设置 GraphQLWithRetry 的最大重试次数（不含首次请求）。
func WithMaxRetry(n int) Option {
	return func(c *Config) {
		if c == nil {
			return
		}
		c.MaxRetry = n
	}
}

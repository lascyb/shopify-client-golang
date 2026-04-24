package auth

import (
	"context"
	"fmt"
	"strings"
)

// StaticAuth 提供固定 Admin API access token。
type StaticAuth struct {
	domain      string
	accessToken string
}

// NewStaticAuth 创建固定 token 的 TokenProvider。
func NewStaticAuth(shopDomain, accessToken string) *StaticAuth {
	return &StaticAuth{
		domain:      shopDomain,
		accessToken: accessToken,
	}
}

// AccessToken 返回固定 token。
func (a *StaticAuth) AccessToken(ctx context.Context) (string, error) {
	token := strings.TrimSpace(a.accessToken)
	if token == "" {
		return "", fmt.Errorf("shopify: static access token 为空")
	}
	return token, nil
}
func (a *StaticAuth) Domain() string {
	return a.domain
}

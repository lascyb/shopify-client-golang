// Package auth 提供 Shopify Admin API TokenProvider 的实现。
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientCredentialsAuth 使用 client credentials grant 获取短期 Admin access token，并进行缓存与自动刷新。
// 一个实例建议只挂载在一个 Client（即一个 shopDomain）上，确保 token 缓存隔离。
type ClientCredentialsAuth struct {
	domain       string
	clientID     string
	clientSecret string

	expireSkew time.Duration

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time

	httpClient    *http.Client
	tokenEndpoint string
}

type clientCredentialsTokenResponse struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope,omitempty"`
	ExpiresIn   int    `json:"expires_in"`
}

// NewClientCredentialsAuth 创建动态 token 获取器。
func NewClientCredentialsAuth(domain, clientID, clientSecret string) *ClientCredentialsAuth {
	return &ClientCredentialsAuth{
		domain:        domain,
		clientID:      strings.TrimSpace(clientID),
		clientSecret:  strings.TrimSpace(clientSecret),
		expireSkew:    time.Minute,
		httpClient:    &http.Client{},
		tokenEndpoint: fmt.Sprintf("https://%s/admin/oauth/access_token", domain),
	}
}

func (p *ClientCredentialsAuth) Domain() string {
	return p.domain
}

// AccessToken 返回当前可用 token，未到期则直接使用缓存；到期前会刷新。
func (p *ClientCredentialsAuth) AccessToken(ctx context.Context) (string, error) {
	if p == nil {
		return "", fmt.Errorf("shopify: client credentials auth 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查缓存是否有效（提前 expireSkew 刷新）
	if p.accessToken != "" && time.Now().Before(p.expiresAt.Add(-p.expireSkew)) {
		return p.accessToken, nil
	}

	// 参数校验
	if strings.TrimSpace(p.clientID) == "" {
		return "", fmt.Errorf("shopify: clientID 不能为空")
	}
	if strings.TrimSpace(p.clientSecret) == "" {
		return "", fmt.Errorf("shopify: clientSecret 不能为空")
	}
	if strings.TrimSpace(p.domain) == "" {
		return "", fmt.Errorf("shopify: client credentials auth 未绑定 shopDomain")
	}
	if p.httpClient == nil {
		return "", fmt.Errorf("shopify: client credentials auth 未绑定 httpClient")
	}

	// 在锁内刷新 token
	accessToken, expiresAt, err := p.fetchClientCredentialsAccessToken(ctx)
	if err != nil {
		return "", err
	}

	// 更新缓存
	p.accessToken = accessToken
	p.expiresAt = expiresAt

	return accessToken, nil
}

func (p *ClientCredentialsAuth) fetchClientCredentialsAccessToken(ctx context.Context) (string, time.Time, error) {

	values := url.Values{}
	values.Set("grant_type", "client_credentials")
	values.Set("client_id", p.clientID)
	values.Set("client_secret", p.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("shopify: 构建 access token 请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("shopify: 请求 access token 失败：%w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("shopify: 读取 access token 响应失败：%w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("shopify: 获取 access token HTTP 异常(status=%d body=%s)", resp.StatusCode, string(respBytes))
	}

	var tokenResp clientCredentialsTokenResponse
	if err = json.Unmarshal(respBytes, &tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("shopify: 解析 access token 响应失败：%w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", time.Time{}, fmt.Errorf("shopify: access token 为空")
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		// 文档 expires_in 固定为 86399，但这里做兜底。
		expiresIn = int((24 * time.Hour).Seconds())
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return tokenResp.AccessToken, expiresAt, nil
}

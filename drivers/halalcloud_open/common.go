package halalcloudopen

import (
	"sync"
	"time"

	sdkUser "github.com/halalcloud/golang-sdk-lite/halalcloud/services/user"
)

var (
	slicePostErrorRetryInterval = time.Second * 120
	retryTimes                  = 5
)

type halalCommon struct {
	// *AuthService     // 登录信息
	UserInfo         *sdkUser.User // 用户信息
	refreshTokenFunc func(token string) error
	// serv             *AuthService
	configs sync.Map
}

func (m *halalCommon) GetAccessToken() (string, error) {
	value, exists := m.configs.Load("access_token")
	if !exists {
		return "", nil // 如果不存在，返回空字符串
	}
	return value.(string), nil // 返回配置项的值
}

// GetRefreshToken implements ConfigStore.
func (m *halalCommon) GetRefreshToken() (string, error) {
	value, exists := m.configs.Load("refresh_token")
	if !exists {
		return "", nil // 如果不存在，返回空字符串
	}
	return value.(string), nil // 返回配置项的值
}

// SetAccessToken implements ConfigStore.
func (m *halalCommon) SetAccessToken(token string) error {
	m.configs.Store("access_token", token)
	return nil
}

// SetRefreshToken implements ConfigStore.
func (m *halalCommon) SetRefreshToken(token string) error {
	m.configs.Store("refresh_token", token)
	if m.refreshTokenFunc != nil {
		return m.refreshTokenFunc(token)
	}
	return nil
}

// SetToken implements ConfigStore.
func (m *halalCommon) SetToken(accessToken string, refreshToken string, expiresIn int64) error {
	m.configs.Store("access_token", accessToken)
	m.configs.Store("refresh_token", refreshToken)
	m.configs.Store("expires_in", expiresIn)
	if m.refreshTokenFunc != nil {
		return m.refreshTokenFunc(refreshToken)
	}
	return nil
}

// ClearConfigs implements ConfigStore.
func (m *halalCommon) ClearConfigs() error {
	m.configs = sync.Map{} // 清空map
	return nil
}

// DeleteConfig implements ConfigStore.
func (m *halalCommon) DeleteConfig(key string) error {
	_, exists := m.configs.Load(key)
	if !exists {
		return nil // 如果不存在，直接返回
	}
	m.configs.Delete(key) // 删除指定的配置项
	return nil
}

// GetConfig implements ConfigStore.
func (m *halalCommon) GetConfig(key string) (string, error) {
	value, exists := m.configs.Load(key)
	if !exists {
		return "", nil // 如果不存在，返回空字符串
	}
	return value.(string), nil // 返回配置项的值
}

// ListConfigs implements ConfigStore.
func (m *halalCommon) ListConfigs() (map[string]string, error) {
	configs := make(map[string]string)
	m.configs.Range(func(key, value interface{}) bool {
		configs[key.(string)] = value.(string) // 将每个配置项添加到map中
		return true                            // 继续遍历
	})
	return configs, nil // 返回所有配置项
}

// SetConfig implements ConfigStore.
func (m *halalCommon) SetConfig(key string, value string) error {
	m.configs.Store(key, value) // 使用Store方法设置或更新配置项
	return nil                  // 成功设置配置项后返回nil
}

func NewHalalCommon() *halalCommon {
	return &halalCommon{
		configs: sync.Map{},
	}
}

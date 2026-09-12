package configmgmt

import (
	"errors"
	"time"

	"github.com/fatedier/frp/client/proxy"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

var (
	ErrInvalidArgument = errors.New("参数无效")
	ErrNotFound        = errors.New("未找到")
	ErrConflict        = errors.New("已存在冲突")
	ErrStoreDisabled   = errors.New("存储功能未启用")
	ErrApplyConfig     = errors.New("应用配置失败")
)

type ConfigManager interface {
	ReloadFromFile(strict bool) error

	ReadConfigFile() (string, error)
	WriteConfigFile(content []byte) error

	GetProxyStatus() []*proxy.WorkingStatus
	IsStoreProxyEnabled(name string) bool
	StoreEnabled() bool

	GetProxyConfig(name string) (v1.ProxyConfigurer, bool)
	GetVisitorConfig(name string) (v1.VisitorConfigurer, bool)

	ListStoreProxies() ([]v1.ProxyConfigurer, error)
	GetStoreProxy(name string) (v1.ProxyConfigurer, error)
	CreateStoreProxy(cfg v1.ProxyConfigurer) (v1.ProxyConfigurer, error)
	UpdateStoreProxy(name string, cfg v1.ProxyConfigurer) (v1.ProxyConfigurer, error)
	DeleteStoreProxy(name string) error

	ListStoreVisitors() ([]v1.VisitorConfigurer, error)
	GetStoreVisitor(name string) (v1.VisitorConfigurer, error)
	CreateStoreVisitor(cfg v1.VisitorConfigurer) (v1.VisitorConfigurer, error)
	UpdateStoreVisitor(name string, cfg v1.VisitorConfigurer) (v1.VisitorConfigurer, error)
	DeleteStoreVisitor(name string) error

	GracefulClose(d time.Duration)
}

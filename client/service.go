// Copyright 2017 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatedier/golib/crypto"
	"github.com/samber/lo"

	"github.com/fatedier/frp/client/proxy"
	"github.com/fatedier/frp/pkg/auth"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/fatedier/frp/pkg/policy/security"
	httppkg "github.com/fatedier/frp/pkg/util/http"
	"github.com/fatedier/frp/pkg/util/log"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/wait"
	"github.com/fatedier/frp/pkg/util/xlog"
)

func init() {
	crypto.DefaultSalt = "frp"
	// Disable quic-go's receive buffer warning.
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	// Disable quic-go's ECN support by default. It may cause issues on certain operating systems.
	if os.Getenv("QUIC_GO_DISABLE_ECN") == "" {
		os.Setenv("QUIC_GO_DISABLE_ECN", "true")
	}
}

type cancelErr struct {
	Err error
}

func (e cancelErr) Error() string {
	return e.Err.Error()
}

// ServiceOptions contains options for creating a new client service.
type ServiceOptions struct {
	Common *v1.ClientCommonConfig

	// ConfigSourceAggregator manages internal config and optional store sources.
	// It is required for creating a Service.
	ConfigSourceAggregator *source.Aggregator

	UnsafeFeatures *security.UnsafeFeatures

	// ConfigFilePath is the path to the configuration file used to initialize.
	// If it is empty, it means that the configuration file is not used for initialization.
	// It may be initialized using command line parameters or called directly.
	ConfigFilePath string

	// ConnectorCreator is a function that creates a new connector to make connections to the server.
	// The Connector shields the underlying connection details, whether it is through TCP or QUIC connection,
	// and regardless of whether multiplexing is used.
	//
	// If it is not set, the default frpc connector will be used.
	// By using a custom Connector, it can be used to implement a VirtualClient, which connects to frps
	// through a pipe instead of a real physical connection.
	ConnectorCreator func(context.Context, *v1.ClientCommonConfig) Connector

	// HandleWorkConnCb is a callback function that is called when a new work connection is created.
	//
	// If it is not set, the default frpc implementation will be used.
	HandleWorkConnCb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool
}

// setServiceOptionsDefault sets the default values for ServiceOptions.
func setServiceOptionsDefault(options *ServiceOptions) error {
	if options.Common != nil {
		if err := options.Common.Complete(); err != nil {
			return err
		}
	}
	if options.ConnectorCreator == nil {
		options.ConnectorCreator = NewConnector
	}
	return nil
}

// Service is the client service that connects to frps and provides proxy services.
type Service struct {
	ctlMu sync.RWMutex
	// Stores gracefulShutdownDuration independently from ctlMu, because the
	// graceful shutdown wait may hold ctlMu for an arbitrary duration.
	gracefulShutdownDuration atomic.Int64
	// manager control connection with server
	ctl *Control
	// Uniq id got from frps, it will be attached to loginMsg.
	runID string

	// Auth runtime and encryption materials
	auth *auth.ClientAuth

	// web server for admin UI and apis
	webServer *httppkg.Server

	cfgMu sync.RWMutex
	// reloadMu serializes reload transactions to keep reloadCommon and applied
	// config in sync across concurrent API operations.
	reloadMu sync.Mutex
	common   *v1.ClientCommonConfig
	// reloadCommon is used for filtering/defaulting during config-source reloads.
	// It can be updated by /api/reload without mutating startup-only common behavior.
	reloadCommon *v1.ClientCommonConfig
	proxyCfgs    []v1.ProxyConfigurer
	visitorCfgs  []v1.VisitorConfigurer

	// aggregator manages multiple configuration sources.
	// When set, the service watches for config changes and reloads automatically.
	aggregator   *source.Aggregator
	configSource *source.ConfigSource
	storeSource  *source.StoreSource

	unsafeFeatures *security.UnsafeFeatures

	// The configuration file used to initialize this client, or an empty
	// string if no configuration file was used.
	configFilePath string

	// service context
	ctx context.Context
	// call cancel to stop service
	cancel context.CancelCauseFunc

	connectorCreator func(context.Context, *v1.ClientCommonConfig) Connector
	handleWorkConnCb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool

	// exitErr records the fatal error that caused the service to stop.
	// Run returns it so the process exits with a non-zero code instead of
	// calling os.Exit deep inside goroutines (which would kill tests too).
	exitMu  sync.Mutex
	exitErr error
}

func NewService(options ServiceOptions) (*Service, error) {
	if err := setServiceOptionsDefault(&options); err != nil {
		return nil, err
	}

	authRuntime, err := auth.BuildClientAuth(&options.Common.Auth)
	if err != nil {
		return nil, err
	}

	if options.ConfigSourceAggregator == nil {
		return nil, fmt.Errorf("config source aggregator is required")
	}

	configSource := options.ConfigSourceAggregator.ConfigSource()
	storeSource := options.ConfigSourceAggregator.StoreSource()

	proxyCfgs, visitorCfgs, loadErr := options.ConfigSourceAggregator.Load()
	if loadErr != nil {
		return nil, fmt.Errorf("failed to load config from aggregator: %w", loadErr)
	}
	proxyCfgs, visitorCfgs = config.FilterClientConfigurers(options.Common, proxyCfgs, visitorCfgs)
	proxyCfgs = config.CompleteProxyConfigurers(proxyCfgs)
	visitorCfgs = config.CompleteVisitorConfigurers(visitorCfgs)

	// Create the web server after all fallible steps so its listener is not
	// leaked when an earlier error causes NewService to return.
	var webServer *httppkg.Server
	if options.Common.WebServer.Port > 0 {
		ws, err := httppkg.NewServer(options.Common.WebServer)
		if err != nil {
			return nil, err
		}
		webServer = ws
	}

	s := &Service{
		ctx:              context.Background(),
		auth:             authRuntime,
		webServer:        webServer,
		common:           options.Common,
		reloadCommon:     options.Common,
		configFilePath:   options.ConfigFilePath,
		unsafeFeatures:   options.UnsafeFeatures,
		proxyCfgs:        proxyCfgs,
		visitorCfgs:      visitorCfgs,
		aggregator:       options.ConfigSourceAggregator,
		configSource:     configSource,
		storeSource:      storeSource,
		connectorCreator: options.ConnectorCreator,
		handleWorkConnCb: options.HandleWorkConnCb,
	}

	if webServer != nil {
		webServer.RouteRegister(s.registerRouteHandlers)
	}
	return s, nil
}

func (svr *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	svr.ctx = xlog.NewContext(ctx, xlog.FromContextSafe(ctx))
	svr.cancel = cancel

	// set custom DNSServer
	if svr.common.DNSServer != "" {
		netpkg.SetDefaultDNSAddress(svr.common.DNSServer)
	}

	if svr.webServer != nil {
		webServer := svr.webServer
		go func() {
			log.Infof("admin server listen on %s", webServer.Address())
			if err := webServer.Run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Warnf("admin server exit with error: %v", err)
			}
		}()
	}

	// first login to frps
	svr.loopLoginUntilSuccess(10*time.Second, lo.FromPtr(svr.common.LoginFailExit))
	if svr.ctl == nil {
		cancelCause := cancelErr{}
		_ = errors.As(context.Cause(svr.ctx), &cancelCause)
		svr.stop()
		return fmt.Errorf("login to the server failed: %v. With loginFailExit enabled, no additional retries will be attempted", cancelCause.Err)
	}

	go svr.keepControllerWorking()

	<-svr.ctx.Done()
	svr.stop()
	return svr.getExitError()
}

// exitWithError records a fatal error and stops the service.
// Run will return the recorded error so the process exits non-zero.
func (svr *Service) exitWithError(err error) {
	svr.exitMu.Lock()
	if svr.exitErr == nil {
		svr.exitErr = err
	}
	svr.exitMu.Unlock()
	svr.cancel(cancelErr{Err: err})
}

func (svr *Service) getExitError() error {
	svr.exitMu.Lock()
	defer svr.exitMu.Unlock()
	return svr.exitErr
}

func (svr *Service) keepControllerWorking() {
	xl := xlog.FromContextSafe(svr.ctx)

	reconnectDelays := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
	}
	reconnectCounts := 0

	for {
		svr.ctlMu.RLock()
		ctl := svr.ctl
		svr.ctlMu.RUnlock()
		if ctl == nil {
			return
		}
		<-ctl.Done()

		if svr.ctx.Err() != nil {
			return
		}

		if reconnectCounts >= len(reconnectDelays) {
			err := fmt.Errorf("重连已失败 %d 次，客户端即将退出", reconnectCounts)
			xl.Errorf("%v", err)
			svr.exitWithError(err)
			return
		}

		wait := reconnectDelays[reconnectCounts]
		xl.Infof("第 %d 次重连，等待 %v 后尝试连接", reconnectCounts+1, wait)
		time.Sleep(wait)
		reconnectCounts++

		xl.Infof("尝试重新连接至服务器")
		err := svr.tryLogin()
		if err != nil {
			xl.Warnf("重连失败: %v", err)
		} else {
			reconnectCounts = 0
		}
	}
}

func (svr *Service) loopLoginUntilSuccess(maxInterval time.Duration, firstLoginExit bool) {
	xl := xlog.FromContextSafe(svr.ctx)

	loginFunc := func() (bool, error) {
		err := svr.tryLogin()
		if err != nil {
			xl.Warnf("无法连接至服务器: %v", err)
			if firstLoginExit {
				svr.printLoginErrorHint(err)
				svr.exitWithError(err)
				return true, nil
			}
			return false, err
		}
		return true, nil
	}

	// try to reconnect to server until success
	wait.BackoffUntil(loginFunc, wait.NewFastBackoffManager(
		wait.FastBackoffOptions{
			Duration:    time.Second,
			Factor:      2,
			Jitter:      0.1,
			MaxDuration: maxInterval,
		}), true, svr.ctx.Done())
}

func (svr *Service) tryLogin() error {
	xl := xlog.FromContextSafe(svr.ctx)
	xl.Infof("try to connect to server...")
	dialer := &controlSessionDialer{
		ctx:              svr.ctx,
		common:           svr.common,
		auth:             svr.auth,
		connectorCreator: svr.connectorCreator,
	}
	sessionCtx, err := dialer.Dial(svr.runID)
	if err != nil {
		return err
	}

	svr.runID = sessionCtx.RunID
	xl.AddPrefix(xlog.LogPrefix{Name: "runID", Value: svr.runID})
	xl.Infof("login to server success, get run id [%s]", svr.runID)

	svr.cfgMu.RLock()
	proxyCfgs := svr.proxyCfgs
	visitorCfgs := svr.visitorCfgs
	svr.cfgMu.RUnlock()

	ctl, err := NewControl(svr.ctx, sessionCtx)
	if err != nil {
		sessionCtx.Conn.Close()
		sessionCtx.Connector.Close()
		xl.Errorf("new control error: %v", err)
		return err
	}
	ctl.SetInWorkConnCallback(svr.handleWorkConnCb)

	ctl.Run(proxyCfgs, visitorCfgs)
	// close and replace previous control
	svr.ctlMu.Lock()
	if svr.ctl != nil {
		svr.ctl.Close()
	}
	svr.ctl = ctl
	svr.ctlMu.Unlock()
	return nil
}

func (svr *Service) printLoginErrorHint(err error) {
	xl := xlog.FromContextSafe(svr.ctx)
	msg := err.Error()
	switch {
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "EOF"):
		xl.Warnf("请尝试将配置文件中tls_enable = false改为tls_enable = true再启动，如果依旧无法启动，则为上层防火墙拦截，请更换设备。")
	case strings.Contains(msg, "invalid port"):
		xl.Warnf("无效的节点端口，如果您没有随意更改配置文件，请前往交流群提交问题。您可以暂时更换节点解决")
	case strings.Contains(msg, "token in login doesn't match token from configuration"):
		xl.Warnf("节点TOKEN错误，如果您没有随意更改配置文件，请前往交流群提交问题。您可以暂时更换节点解决")
	case strings.Contains(msg, "i/o deadline reached"):
		xl.Warnf("请尝试将配置文件中tls_enable = false改为tls_enable = true再启动，如果依旧无法启动，则为上层防火墙拦截，请更换设备。或更换节点。")
	case strings.Contains(msg, "dial tcp 127.0.0.1:7000: connectex: No connection could be made because the target machine actively refused it."):
		xl.Warnf("您尚未更改配置文件，请更改配置文件(frpc.toml)后再启动隧道。更改完后需要按Ctrl+S保存。")
	case strings.Contains(msg, "connectex: No connection could be made because the target machine actively refused it."):
		xl.Warnf("此节点可能已离线，或您的网络连不上此节点，请更换节点后再启动。如若更换节点无用，请加入交流群询问。")
	}
}

func (svr *Service) UpdateAllConfigurer(proxyCfgs []v1.ProxyConfigurer, visitorCfgs []v1.VisitorConfigurer) error {
	svr.cfgMu.Lock()
	svr.proxyCfgs = proxyCfgs
	svr.visitorCfgs = visitorCfgs
	svr.cfgMu.Unlock()

	svr.ctlMu.RLock()
	ctl := svr.ctl
	svr.ctlMu.RUnlock()

	if ctl != nil {
		return svr.ctl.UpdateAllConfigurer(proxyCfgs, visitorCfgs)
	}
	return nil
}

func (svr *Service) UpdateConfigSource(
	common *v1.ClientCommonConfig,
	proxyCfgs []v1.ProxyConfigurer,
	visitorCfgs []v1.VisitorConfigurer,
) error {
	svr.reloadMu.Lock()
	defer svr.reloadMu.Unlock()

	cfgSource := svr.configSource
	if cfgSource == nil {
		return fmt.Errorf("config source is not available")
	}

	if err := cfgSource.ReplaceAll(proxyCfgs, visitorCfgs); err != nil {
		return err
	}

	// Non-atomic update semantics: source has been updated at this point.
	// Even if reload fails below, keep this common config for subsequent reloads.
	svr.cfgMu.Lock()
	svr.reloadCommon = common
	svr.cfgMu.Unlock()

	if err := svr.reloadConfigFromSourcesLocked(); err != nil {
		return err
	}
	return nil
}

func (svr *Service) Close() {
	svr.GracefulClose(time.Duration(0))
}

func (svr *Service) GracefulClose(d time.Duration) {
	svr.gracefulShutdownDuration.Store(int64(d))
	svr.cancel(nil)
}

func (svr *Service) stop() {
	// Coordinate shutdown with reload/update paths that read source pointers.
	svr.reloadMu.Lock()
	if svr.aggregator != nil {
		svr.aggregator = nil
	}
	svr.configSource = nil
	svr.storeSource = nil
	svr.reloadMu.Unlock()

	svr.ctlMu.Lock()
	defer svr.ctlMu.Unlock()
	if svr.ctl != nil {
		d := time.Duration(svr.gracefulShutdownDuration.Load())
		svr.ctl.GracefulClose(d)
		svr.ctl = nil
	}
	if svr.webServer != nil {
		svr.webServer.Close()
		svr.webServer = nil
	}
}

func (svr *Service) getProxyStatus(name string) (*proxy.WorkingStatus, bool) {
	svr.ctlMu.RLock()
	ctl := svr.ctl
	svr.ctlMu.RUnlock()

	if ctl == nil {
		return nil, false
	}
	return ctl.pm.GetProxyStatus(name)
}

func (svr *Service) getVisitorCfg(name string) (v1.VisitorConfigurer, bool) {
	svr.ctlMu.RLock()
	ctl := svr.ctl
	svr.ctlMu.RUnlock()

	if ctl == nil {
		return nil, false
	}
	return ctl.vm.GetVisitorCfg(name)
}

func (svr *Service) StatusExporter() StatusExporter {
	return &statusExporterImpl{
		getProxyStatusFunc: svr.getProxyStatus,
	}
}

type StatusExporter interface {
	GetProxyStatus(name string) (*proxy.WorkingStatus, bool)
}

type statusExporterImpl struct {
	getProxyStatusFunc func(name string) (*proxy.WorkingStatus, bool)
}

func (s *statusExporterImpl) GetProxyStatus(name string) (*proxy.WorkingStatus, bool) {
	return s.getProxyStatusFunc(name)
}

func (svr *Service) reloadConfigFromSources() error {
	svr.reloadMu.Lock()
	defer svr.reloadMu.Unlock()
	return svr.reloadConfigFromSourcesLocked()
}

func (svr *Service) reloadConfigFromSourcesLocked() error {
	aggregator := svr.aggregator
	if aggregator == nil {
		return errors.New("config aggregator is not initialized")
	}

	svr.cfgMu.RLock()
	reloadCommon := svr.reloadCommon
	svr.cfgMu.RUnlock()

	proxies, visitors, err := aggregator.Load()
	if err != nil {
		return fmt.Errorf("reload config from sources failed: %w", err)
	}

	proxies, visitors = config.FilterClientConfigurers(reloadCommon, proxies, visitors)
	proxies = config.CompleteProxyConfigurers(proxies)
	visitors = config.CompleteVisitorConfigurers(visitors)

	// Atomically replace the entire configuration
	if err := svr.UpdateAllConfigurer(proxies, visitors); err != nil {
		return err
	}
	return nil
}

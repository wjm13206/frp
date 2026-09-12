// Copyright 2018 fatedier, fatedier@gmail.com
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

package sub

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/api"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/version"
)

var (
	cfgFile          string
	cfgDir           string
	cfgToken         string
	cfgProxyid       string
	showVersion      bool
	strictConfigMode bool
	allowUnsafe      []string
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "./frpc.toml", "config file of frpc (support toml/yaml/json, ini is legacy)")
	rootCmd.PersistentFlags().StringVarP(&cfgDir, "config_dir", "", "", "config directory, run one frpc service for each file in config directory")
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "version of frpc")
	rootCmd.PersistentFlags().BoolVarP(&strictConfigMode, "strict_config", "", true, "strict config parsing mode, unknown fields will cause an errors")

	rootCmd.PersistentFlags().StringSliceVarP(&allowUnsafe, "allow-unsafe", "", []string{},
		fmt.Sprintf("allowed unsafe features, one or more of: %s", strings.Join(security.ClientUnsafeFeatures, ", ")))
	rootCmd.PersistentFlags().StringVarP(&cfgToken, "token", "u", "", "The Token of ChmlFrp")
	rootCmd.PersistentFlags().StringVarP(&cfgProxyid, "id", "p", "", "The ProxyID of ChmlFrp")
}

var rootCmd = &cobra.Command{
	Use:   "frpc",
	Short: "frpc is the client of frp (https://github.com/fatedier/frp)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Println(version.Full())
			return nil
		}

		log.Infof("欢迎使用ChmlFrp映射客户端!")

		unsafeFeatures := security.NewUnsafeFeatures(allowUnsafe)

		// If cfgDir is not empty, run multiple frpc service for each config file in cfgDir.
		// Note that it's only designed for testing. It's not guaranteed to be stable.
		if cfgDir != "" {
			_ = runMultipleClients(cfgDir, unsafeFeatures)
			return nil
		}

		// 未显式指定 -c 时，自动发现 toml/yaml/json/ini（兼容 Frpc.toml 等大小写变体）。
		cfgFile = resolveConfigFile(cmd, cfgFile)

		// 非 API 拉取模式下，配置文件不存在时给出友好提示，而不是直接报 open 错误。
		if cfgToken == "" || cfgProxyid == "" {
			if _, err := os.Stat(cfgFile); err != nil && os.IsNotExist(err) {
				fmt.Printf("找不到配置文件 [%s]，已自动查找 %s，"+
					"请确认目录下存在 frpc.toml（或 Frpc.toml）/frpc.yaml/frpc.yml/frpc.json/frpc.ini，"+
					"或用 -c 指定配置文件路径\n",
					cfgFile, strings.Join(defaultConfigCandidates(), ", "))
				os.Exit(1)
			}
		}

		// 如果提供了 ChmlFrp Token 和 ProxyID，从 API 获取配置文件
		if cfgToken != "" && cfgProxyid != "" {
			log.Infof("从ChmlFrp API获取配置文件...")
			s, err := api.NewService("https://cf-v2.uapis.cn/cfg")
			if err != nil {
				log.Warnf("初始化API服务失败，错误: %s", err)
			}

			var ids []string
			if strings.Contains(cfgProxyid, ",") {
				ids = strings.Split(cfgProxyid, ",")
			} else {
				ids = []string{cfgProxyid}
			}

			cfg, err := s.EZStartGetCfg(cfgToken, strings.Join(ids, ","))
			if err != nil {
				log.Warnf("获取配置文件失败，err: %s", err)
				os.Exit(1)
			}

			file, err := os.OpenFile(cfgFile, os.O_RDWR|os.O_TRUNC|os.O_CREATE, 0o777)
			if err != nil {
				log.Warnf("打开文件失败，错误: %s", err)
				os.Exit(1)
			}
			_, err = file.WriteString(cfg)
			file.Close()
			if err != nil {
				log.Warnf("写入配置文件失败, Err: %s", err)
				os.Exit(1)
			}
			log.Infof("已写入配置文件: %s", cfgFile)
		}

		// Do not show command usage here.
		err := runClient(cfgFile, unsafeFeatures)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return nil
	},
}

// defaultConfigCandidates 返回未指定 -c 时的自动发现顺序，toml 优先，ini 兜底。
// 包含 Frpc.toml 等大小写变体以兼容 Windows 用户习惯及 Linux 大小写敏感文件系统。
func defaultConfigCandidates() []string {
	return []string{
		"./frpc.toml",
		"./Frpc.toml",
		"./FRPC.toml",
		"./frpc.yaml",
		"./frpc.yml",
		"./frpc.json",
		"./frpc.ini",
		"./Frpc.ini",
	}
}

func configFlagChanged(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Flags().Changed("config") {
		return true
	}
	if cmd.PersistentFlags().Changed("config") {
		return true
	}
	return false
}

// resolveConfigFile 在用户未显式指定 -c 且默认文件不存在时，自动查找同目录下的
// frpc.toml/Frpc.toml/yaml/yml/json/ini，找到即返回，未找到则返回原路径由调用方报错。
func resolveConfigFile(cmd *cobra.Command, cfg string) string {
	if configFlagChanged(cmd) {
		return cfg
	}
	if cfg == "" {
		cfg = "./frpc.toml"
	}
	if _, err := os.Stat(cfg); err == nil {
		return cfg
	}
	for _, candidate := range defaultConfigCandidates() {
		if candidate == cfg {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			log.Infof("未指定 -c，自动使用配置文件: %s", candidate)
			return candidate
		}
	}
	return cfg
}

func runMultipleClients(cfgDir string, unsafeFeatures *security.UnsafeFeatures) error {
	var wg sync.WaitGroup
	err := filepath.WalkDir(cfgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		wg.Add(1)
		time.Sleep(time.Millisecond)
		go func() {
			defer wg.Done()
			err := runClient(path, unsafeFeatures)
			if err != nil {
				fmt.Printf("frpc service error for config file [%s]\n", path)
			}
		}()
		return nil
	})
	wg.Wait()
	return err
}

func Execute() {
	rootCmd.SetGlobalNormalizationFunc(config.WordSepNormalizeFunc)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func handleTermSignal(svr *client.Service) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	svr.GracefulClose(500 * time.Millisecond)
}

func runClient(cfgFilePath string, unsafeFeatures *security.UnsafeFeatures) error {
	// Load configuration
	result, err := config.LoadClientConfigResult(cfgFilePath, strictConfigMode)
	if err != nil {
		return err
	}
	if result.IsLegacyFormat {
		fmt.Printf("WARNING: ini format is deprecated and the support will be removed in the future, " +
			"please use yaml/json/toml format instead!\n")
	}

	return runClientWithAggregator(result, unsafeFeatures, cfgFilePath)
}

// runClientWithAggregator runs the client using the internal source aggregator.
func runClientWithAggregator(result *config.ClientConfigLoadResult, unsafeFeatures *security.UnsafeFeatures, cfgFilePath string) error {
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(result.Proxies, result.Visitors); err != nil {
		return fmt.Errorf("failed to set config source: %w", err)
	}

	var storeSource *source.StoreSource

	if result.Common.Store.IsEnabled() {
		storePath := result.Common.Store.Path
		if storePath != "" && cfgFilePath != "" && !filepath.IsAbs(storePath) {
			storePath = filepath.Join(filepath.Dir(cfgFilePath), storePath)
		}

		s, err := source.NewStoreSource(source.StoreSourceConfig{
			Path: storePath,
		})
		if err != nil {
			return fmt.Errorf("failed to create store source: %w", err)
		}
		storeSource = s
	}

	aggregator := source.NewAggregator(configSource)
	if storeSource != nil {
		aggregator.SetStoreSource(storeSource)
	}

	proxyCfgs, visitorCfgs, err := aggregator.Load()
	if err != nil {
		return fmt.Errorf("failed to load config from sources: %w", err)
	}

	proxyCfgs, visitorCfgs = config.FilterClientConfigurers(result.Common, proxyCfgs, visitorCfgs)
	proxyCfgs = config.CompleteProxyConfigurers(proxyCfgs)
	visitorCfgs = config.CompleteVisitorConfigurers(visitorCfgs)

	warning, err := validation.ValidateAllClientConfig(result.Common, proxyCfgs, visitorCfgs, unsafeFeatures)
	if warning != nil {
		fmt.Printf("WARNING: %v\n", warning)
	}
	if err != nil {
		return err
	}

	return startServiceWithAggregator(result.Common, aggregator, unsafeFeatures, cfgFilePath)
}

func startServiceWithAggregator(
	cfg *v1.ClientCommonConfig,
	aggregator *source.Aggregator,
	unsafeFeatures *security.UnsafeFeatures,
	cfgFile string,
) error {
	log.InitLogger(cfg.Log.To, cfg.Log.Level, int(cfg.Log.MaxDays), cfg.Log.DisablePrintColor)

	if cfgFile != "" {
		log.Infof("start frpc service for config file [%s] with aggregated configuration", cfgFile)
		defer log.Infof("frpc service for config file [%s] stopped", cfgFile)
	}
	svr, err := client.NewService(client.ServiceOptions{
		Common:                 cfg,
		ConfigSourceAggregator: aggregator,
		UnsafeFeatures:         unsafeFeatures,
		ConfigFilePath:         cfgFile,
	})
	if err != nil {
		return err
	}

	shouldGracefulClose := cfg.Transport.Protocol == "kcp" || cfg.Transport.Protocol == "quic"
	if shouldGracefulClose {
		go handleTermSignal(svr)
	}
	return svr.Run(context.Background())
}

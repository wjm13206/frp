# ChmlFrp-fork

本项目是 [ChmlFrp](https://github.com/TechCat-Team/ChmlFrp-Frp) 客户端的fork版本，基于 frp v0.71.0 构建

## 与旧版 ChmlFrp 客户端的区别

旧版 ChmlFrp 客户端基于 frp v0.51.2，本项目基于 frp v0.71.0。以下是主要区别：

### 基础架构升级

| 项目 | 旧版 (ChmlFrp-Frp) | 新版 (本项目) |
|------|-------------------|---------------|
| 基础 frp 版本 | v0.51.2 | v0.71.0 |
| Go 版本 | ~1.20 | 1.25 |
| 配置格式 | 仅 ini | yaml / json / toml / ini (legacy) |
| 配置系统 | v0 旧版 | v1 全新配置系统 |
| 版本号 | ChmlFrp-0.51.2_251023 | ChmlFrp-0.71.0_251023 |


### 新增

- **多配置格式**：支持 yaml / json / toml
- **配置源聚合器**：支持内部配置源与外部存储源聚合加载
- **安全策略控制**：通过 `--allow-unsafe` 控制不安全特性的启用
- **TCP 多路复用优化**：更完善的 TCP Mux 连接管理
- **严格配置模式**：`--strict_config` 严格校验未知字段



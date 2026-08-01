# 更新日志

本文件记录项目的重要变更，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布

暂无。

## 0.3.0 - 2026-07-30

### 新增

- 基于 CPA 原生 Scheduler 的一次性 token 固定认证机制。
- Management API 状态、dry-run 和手动运行接口。
- 每日多时间点调度与认证过滤。
- 可恢复的临时 auth priority 覆盖事务。
- Windows、Linux 和 macOS 动态库构建支持。
- 标准化中文仓库文档、贡献流程、安全策略和 GitHub 模板。
- GitHub Actions 持续集成、多平台 Releases 发布流程和 SHA-256 校验文件。
- 本地敏感信息检查脚本。

### 变更

- 示例配置默认关闭临时优先级覆盖，降低首次部署风险。
- 构建脚本支持注入版本号并生成版本化动态库文件名。

### 安全

- 认证 JSON 不进入运行报告或日志。
- 上游执行错误对外脱敏。
- 使用 SHA-256 并发检查和事务 marker 恢复认证 priority。

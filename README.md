# CPA 自动刷新配额插件

`cpa-auto-refresh-quota` 是一个可由原版 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 直接加载的原生动态插件。它会按照配置的时区和每日时间点，筛选认证记录，并通过 CPA 向指定模型发送一条轻量消息。

当前插件版本为 `0.3.0`，已针对 CLIProxyAPI `v7.2.102` 至 `v7.2.104` 的插件宿主进行验证。CPA 本体不需要修改源码，也不需要重新构建。

> [!IMPORTANT]
> 本项目不会要求、保存或上传任何 API Key、管理密钥或认证文件。请勿把真实的 `config.yaml`、`.env`、认证 JSON、私钥、日志或抓包内容提交到仓库或公开 Issue。

## 主要能力

- 在每天多个时间点自动执行，也支持通过 Management API 手动触发。
- 按 provider、`auth_index`、物理文件和可用状态筛选认证。
- 使用 CPA 原生 Scheduler 能力，把每次请求固定到目标认证。
- 支持 dry-run，在不调用模型的情况下检查候选范围。
- 可选地临时对齐低优先级认证，并在请求结束或下次启动时恢复。
- 不记录认证 JSON、访问令牌、刷新令牌或上游响应正文。

## 支持的平台

插件使用 Go 的 `c-shared` 构建模式，动态库必须与运行 CPA 的操作系统和 CPU 架构一致。

| 系统 | 动态库 | 建议目录 |
| --- | --- | --- |
| Windows | `.dll` | `plugins/windows/amd64/` |
| Linux | `.so` | `plugins/linux/amd64/` 或 `plugins/linux/arm64/` |
| macOS | `.dylib` | `plugins/darwin/<架构>/` |

正式构建产物只通过 GitHub Releases 发布，不提交到源码仓库。本地 `dist/` 中已有的构建结果会被 Git 忽略。

## 快速开始

1. 从 [GitHub Releases](https://github.com/PeacherMZ/cpa-auto-refresh-quota/releases) 下载与系统和架构匹配的压缩包，并校验 `SHA256SUMS`。
2. 停止所有正在运行的 CPA 进程。
3. 把压缩包中的 `plugins` 目录解压到一个新的插件根目录。
4. 将 [`config.example.yaml`](config.example.yaml) 中的 `plugins` 段合并到 CPA 的 `config.yaml`。
5. 修改 `model`、`message`、时间点和认证过滤条件，首次部署保持 `schedule_enabled: false`。
6. 完整启动 CPA，先执行 dry-run，再只选一个测试认证进行真实调用。
7. 验证通过后再启用自动调度。

查询插件状态：

```bash
curl -fsS \
  -H "Authorization: Bearer YOUR_MANAGEMENT_KEY" \
  http://127.0.0.1:8317/v0/management/plugins/cpa-auto-refresh-quota/status
```

执行 dry-run：

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer YOUR_MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"dry_run":true}' \
  http://127.0.0.1:8317/v0/management/plugins/cpa-auto-refresh-quota/run
```

完整安装、验证、更新和故障排查步骤见[部署指南](docs/DEPLOYMENT.md)。

## 最小配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-auto-refresh-quota:
      enabled: true
      priority: 10000

      schedule_enabled: false
      timezone: "Asia/Shanghai"
      times: ["08:00", "12:00", "20:00"]

      model: "请替换为 CPA 可用的模型"
      message: "Reply with OK only."
      entry_protocol: "openai"
      exit_protocol: "openai"
      max_tokens: 16

      providers: []
      include_auth_indices: []
      exclude_auth_indices: []
      physical_files_only: true
      skip_unavailable: true

      temporary_priority_override: false
      priority_sync_timeout: "5s"
      request_timeout: "2m"
      delay_between_auths: "1s"
```

各字段、默认值和风险边界见[配置说明](docs/CONFIGURATION.md)。

## 自行构建

Windows：

```powershell
.\scripts\build.ps1
```

Linux 或 macOS：

```bash
./scripts/build.sh
```

构建需要 Go、CGO 和目标平台可用的 C 编译器。跨系统构建还需要对应的交叉 C 工具链，因此最稳妥的方式是在目标系统本机构建。完整依赖、命令、Docker 多架构构建和验证方法见[构建指南](docs/BUILD.md)。

## 重要限制

- 不支持 CPA Home 模式，因为该执行路径会绕过插件 Scheduler。
- 本插件应是唯一 Scheduler，或拥有严格最高的插件优先级。
- 处理同一模型的其他 Model Router 可能绕过认证 Scheduler。
- 其他 Request Interceptor 不得删除或改写 `X-CPA-Auto-Refresh-Token`。
- `temporary_priority_override` 会短暂重写物理认证 JSON；首次部署默认关闭。
- 不要让多个 CPA 进程共享同一 `auth_dir` 并同时启用临时优先级功能。

设计依据、原版 ABI 能力和已知边界见[兼容性调查](docs/INVESTIGATION.md)。

## 文档索引

- [部署指南](docs/DEPLOYMENT.md)
- [配置说明](docs/CONFIGURATION.md)
- [构建指南](docs/BUILD.md)
- [发布指南](docs/RELEASE.md)
- [首次创建 GitHub 仓库](docs/REPOSITORY_SETUP.md)
- [安全策略](SECURITY.md)
- [贡献指南](CONTRIBUTING.md)
- [更新日志](CHANGELOG.md)
- [兼容性调查](docs/INVESTIGATION.md)

## 参与贡献

提交代码前请阅读[贡献指南](CONTRIBUTING.md)，并确保测试、格式检查和敏感信息检查全部通过。安全问题请按[安全策略](SECURITY.md)私下报告，不要公开披露可利用细节或真实凭据。

## 许可证

本项目采用 [MIT License](LICENSE)。`LICENSE` 保留标准英文法律文本，以避免翻译造成许可含义偏差。

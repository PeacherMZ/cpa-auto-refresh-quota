# 部署指南

## 部署前检查

- 使用原版 CLIProxyAPI `v7.2.102` 至 `v7.2.104`，或自行验证更高版本的插件 ABI 兼容性。
- CPA 使用本地认证管理模式，不使用 Home 模式。
- 本插件是唯一 Scheduler，或拥有严格最高的插件优先级。
- 没有会绕过认证 Scheduler 的冲突 Model Router。
- CPA 的 `remote-management.secret-key` 已设置，`remote-management.allow-remote` 建议保持 `false`。
- 已备份 CPA 配置和认证目录，但备份文件不放入本仓库。

## 获取并校验产物

从 GitHub Releases 下载与系统和架构匹配的压缩包，同时下载 `SHA256SUMS`。

Linux/macOS 校验：

```bash
sha256sum -c SHA256SUMS --ignore-missing
```

Windows PowerShell 校验单个文件：

```powershell
Get-FileHash .\cpa-auto-refresh-quota-v0.3.0-windows-amd64.zip -Algorithm SHA256
```

把输出与 `SHA256SUMS` 中对应记录比较。

## 安装目录

发布压缩包包含符合 CPA 扫描规则的目录：

```text
plugins/
  windows/amd64/cpa-auto-refresh-quota-v0.3.0.dll
  linux/amd64/cpa-auto-refresh-quota-v0.3.0.so
  linux/arm64/cpa-auto-refresh-quota-v0.3.0.so
  darwin/<架构>/cpa-auto-refresh-quota-v0.3.0.dylib
```

每个压缩包只包含自己的目标平台目录。将其中的 `plugins` 目录解压到一个新的版本化目录，例如：

```text
C:/cpa-plugins-0.3.0/
```

然后在 CPA 配置中使用绝对路径：

```yaml
plugins:
  enabled: true
  dir: "C:/cpa-plugins-0.3.0"
```

版本化动态库仍会映射为插件 ID `cpa-auto-refresh-quota`，配置键必须是：

```text
plugins.configs.cpa-auto-refresh-quota
```

## 首次启动

1. 完整停止所有 CPA 进程。
2. 合并 [`config.example.yaml`](../config.example.yaml) 的配置。
3. 保持 `schedule_enabled: false` 和 `temporary_priority_override: false`。
4. 启动 CPA。
5. 查询插件列表，确认同时声明 `scheduler` 和 `management_api` 能力。

```bash
curl -fsS \
  -H "Authorization: Bearer YOUR_MANAGEMENT_KEY" \
  http://127.0.0.1:8317/v0/management/plugins
```

## 安全验证

查询状态：

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

只测试一个认证：

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer YOUR_MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"auth_indices":["REPLACE_WITH_AUTH_INDEX"]}' \
  http://127.0.0.1:8317/v0/management/plugins/cpa-auto-refresh-quota/run
```

`run` 返回 HTTP 202 和 `run_id`。轮询状态接口，检查 `active_run` 或 `last_run`，并确认没有 `priority_recovery_error`。

## Docker 中使用

不需要重新制作 CPA 镜像。把对应 Linux 动态库目录只读挂载到 CPA 容器的插件目录，并让 `plugins.dir` 指向容器内路径。动态库必须与容器 CPU 架构和 libc 环境兼容。

不要把宿主机的认证目录、真实配置或密钥复制进本项目的 Docker 构建上下文。

## 更新

1. 设置 `schedule_enabled: false`。
2. 确认 `running: false`、`priority_recovery_pending: false`、`priority_recovery_error` 为空。
3. 完整停止 CPA。
4. 把新版本解压到新的版本化插件目录。
5. 修改 `plugins.dir`，不要覆盖运行中已加载的 DLL。
6. 完整启动 CPA，再次执行 dry-run 和单认证测试。

## 卸载

1. 禁用自动调度并等待当前任务结束。
2. 确认没有待恢复的 priority marker。
3. 完整停止 CPA。
4. 从配置中禁用或移除本插件。
5. 再启动 CPA。

如果认证 JSON 中仍存在 `_cpa_auto_refresh_quota_priority_override`，不要直接卸载。应先让对应版本插件启动并完成自动恢复。

## 常见问题

- CPA 找不到插件：检查扩展名、系统、CPU 架构、目录层级、`plugins.enabled`、插件 `enabled` 和 `plugins.dir`。
- 只有 Management API、没有 Scheduler：通常仍在加载旧动态库，请换用新的版本化目录并完整重启。
- `plugin schema version 2 is not supported`：通常加载了旧 DLL 或错误目录；当前插件固定声明兼容 Schema v1。
- `target auth is unavailable to CPA scheduler`：检查模型支持、cooldown、过滤条件和 auth priority；确需处理低优先级认证时再启用临时覆盖。
- `CPA scheduler did not select target auth`：检查 Home 模式、其他 Scheduler、Model Router 和 Request Interceptor。
- `priority_recovery_error` 非空：不要继续自动调度或卸载，先检查认证文件权限、JSON、marker 和 watcher 同步。
- Windows DLL 加载失败：确认 CPA 与 DLL 架构一致，并检查 MinGW/LLVM 运行时兼容性。

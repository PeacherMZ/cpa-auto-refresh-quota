# 配置说明

把 [`config.example.yaml`](../config.example.yaml) 中的 `plugins` 段合并到 CPA 的 `config.yaml`。不要用示例文件覆盖 CPA 的完整配置，也不要把包含真实密钥的 `config.yaml` 复制进本仓库。

## 推荐的首次部署顺序

1. 保持 `schedule_enabled: false`。
2. 保持 `temporary_priority_override: false`。
3. 启动 CPA 并检查插件状态。
4. 执行 dry-run，确认候选认证符合预期。
5. 只用一个 `auth_index` 执行真实测试。
6. 如确实需要处理低优先级认证，再评估并启用临时优先级覆盖。
7. 最后启用自动调度。

## 字段参考

| 字段 | 类型 | 默认/建议值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | 布尔 | `true` | 是否启用本插件。 |
| `priority` | 整数 | `10000` | CPA 只调用最高优先级的 Scheduler。本插件应为唯一 Scheduler，或拥有严格最高值。 |
| `schedule_enabled` | 布尔 | `false` | 是否启用每日自动调度。关闭时仍可查看状态和手动运行。 |
| `timezone` | 字符串 | `Asia/Shanghai` | IANA 时区，也可使用 `Local` 或 `UTC`。 |
| `times` | 字符串数组 | 无 | 每日执行时间，格式为 `HH:MM` 或 `HH:MM:SS`，最多 64 个。 |
| `model` | 字符串 | 无 | CPA 当前能够正常路由的模型。 |
| `message` | 字符串 | 无 | 发给每个目标认证上游的消息。避免包含隐私或秘密。 |
| `entry_protocol` | 字符串 | `openai` | 当前必须为 `openai`，插件会构造非流式 Chat Completions 请求。 |
| `exit_protocol` | 字符串 | `openai` | CPA 使用的目标响应协议，也可使用 CPA 支持的转换格式。 |
| `max_tokens` | 整数 | `16` | 最大响应 token 数，范围 1 至 4096。 |
| `providers` | 字符串数组 | `[]` | provider 白名单；空数组表示不限制。 |
| `include_auth_indices` | 字符串数组 | `[]` | `auth_index` 白名单；空数组表示允许全部候选。 |
| `exclude_auth_indices` | 字符串数组 | `[]` | 在 include 过滤后继续排除的 `auth_index`。 |
| `physical_files_only` | 布尔 | `true` | 是否只处理有物理认证文件的记录。 |
| `skip_unavailable` | 布尔 | `true` | 是否跳过 CPA 标记为 unavailable 的记录。 |
| `temporary_priority_override` | 布尔 | `false` | 是否把低优先级目标临时对齐到当前全局最高档，请求后恢复。 |
| `priority_sync_timeout` | 时长 | `5s` | 等待 CPA watcher 发布临时或恢复后 priority 的最长时间，范围 `100ms` 至 `1m`。 |
| `request_timeout` | 时长 | `2m` | 单个认证模型请求的最长时间，范围 `1s` 至 `30m`。 |
| `delay_between_auths` | 时长 | `1s` | 相邻认证调用间隔，范围 `0` 至 `1h`。 |

## 认证过滤顺序

插件先通过 `host.auth.list` 枚举 CPA 当前知道的认证记录，然后依次应用：

1. disabled、runtime-only、物理文件和 unavailable 条件；
2. `providers` 白名单；
3. `include_auth_indices` 白名单；
4. `exclude_auth_indices` 排除列表；
5. 发送前通过 `host.auth.get_runtime` 再次检查运行时状态。

配置中使用的是 `auth_index`，不是 CPA 内部 AuthID。请从 dry-run 或 Management API 返回结果中获取实际索引，不要猜测。

## 临时优先级覆盖

CPA 会在调用插件 Scheduler 前按 auth priority 分档，只把当前最高档交给 Scheduler。低优先级认证要参与本插件的固定选择，需要显式设置：

```yaml
temporary_priority_override: true
```

启用后，插件会读取目标物理认证 JSON，写入事务 marker，临时调整 `priority`，等待 CPA watcher 生效，完成请求后再恢复。该过程有以下风险：

- 临时窗口内的普通 CPA 流量也可能选中该认证。
- 多个 CPA 进程共享同一 `auth_dir` 时无法提供跨进程事务隔离。
- 文件权限、watcher 延迟或异常退出可能造成恢复等待或错误。

建议只在低流量时段启用，并持续检查状态中的 `priority_recovery_pending` 和 `priority_recovery_error`。

## 配置变更

修改配置后应完整停止并重启 CPA。不要依赖运行期热替换，尤其是 Windows 下已加载的 Go DLL 通常不会在进程内真正卸载。

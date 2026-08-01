# CLIProxyAPI 原版插件机制调查

调查对象：

- 仓库：`router-for-me/CLIProxyAPI`
- tag：`v7.2.104`
- commit：`c9417c8ae9b16fabc0386ca35d36f13bf8b1d678`
- Go module：`github.com/router-for-me/CLIProxyAPI/v7`
- C ABI：v1
- JSON Schema：插件固定声明 v1；兼容 v1/v2 宿主
- 插件实现版本：0.3.0

兼容核对还覆盖 v7.2.102 的 Schema v1：该版本已经具有 Scheduler、Management API、`host.model.execute` 和所需认证 callbacks。v7.2.103 起 Schema 升至 v2，但新增内容与本插件使用的能力无关。

## 结论

原版 CPA 已经能够加载本插件，不需要补丁或重新构建 CPA。

`host.model.execute` 本身没有 `auth_id`，但插件可以同时注册原版 `Scheduler` 能力，并把一次模型调用与目标认证安全地关联：

```text
随机一次性 token
        │
        ├── 内存映射 token -> 目标 auth ID
        │
        └── HostModelExecutionRequest.Headers
                         │
                         ▼
                 scheduler.pick
                         │
                         ▼
            返回 Candidates 中的目标 AuthID
```

无 token 的普通请求保持未处理，CPA 继续使用内置调度。保留头存在但 token 无效，或目标不在候选中时返回错误 envelope，禁止回退到其他认证。

## 动态插件 ABI

`sdk/pluginabi/types.go` 定义：

- `ABIVersion = 1`
- 当前 SDK `SchemaVersion = 2`；本插件只使用 v1 能力，所以固定返回 `schema_version: 1`
- 生命周期方法 `plugin.register`、`plugin.reconfigure`、`plugin.shutdown`
- `scheduler.pick`
- `management.register`、`management.handle`
- `host.model.execute`
- `host.auth.list`、`host.auth.get`、`host.auth.get_runtime`

Schema v2 增加的是请求生命周期完成与主动终止语义；本插件使用的 Scheduler、Management API 和 host callbacks 在 v1 契约中已经存在。因此注册和重配置始终返回 v1。CPA v2 宿主只拒绝高于自身的版本，所以同样接受插件返回 v1。

宿主在 Windows、Linux 和 macOS 分别加载 `.dll`、`.so` 和 `.dylib`，查找导出函数 `cliproxy_plugin_init`。插件返回包含 `call`、`free_buffer` 和 `shutdown` 的函数表。

`internal/pluginhost/platform.go` 表明 CPA 按以下顺序扫描：

```text
<plugins.dir>/<goos>/<goarch>/
<plugins.dir>/
```

文件基名就是插件 ID，所以：

```text
cpa-auto-refresh-quota.dll
```

对应：

```text
plugins.configs.cpa-auto-refresh-quota
```

## 认证枚举

插件使用：

- `host.auth.list`：枚举 CPA 当前知道的认证记录。
- `host.auth.get`：仅在临时 priority 与遗留 marker 恢复时读取精确物理路径和原始 JSON。
- `host.auth.get_runtime`：发送前按 `auth_index` 复查运行时记录。

`sdk/pluginapi/types.go` 的 `HostAuthFileEntry` 提供：

- `id`
- `auth_index`
- `provider`
- `status`
- `disabled`
- `unavailable`
- `runtime_only`
- `source` / `path`
- `priority`

其中 `id` 是 Scheduler 返回的 AuthID；`auth_index` 适合给用户作为 include/exclude 配置。常规枚举只使用这些非秘密字段；启用 `temporary_priority_override` 时，插件会额外通过 `host.auth.get` 取得目标文件的精确 `path` 和原始 JSON，用于临时 priority 事务。

## 为什么 Scheduler 可以固定认证

`HostModelExecutionRequest` 公开字段只有：

```text
entry_protocol
exit_protocol
model
stream
body
headers
query
alt
```

它没有 AuthID。但是原版执行链保留了 Headers：

1. `internal/pluginhost/host_callbacks.go` 的 `modelExecutionRequestFromPlugin` 把 Headers 放入模型请求。
2. `sdk/api/handlers/model_execution.go` 把 Headers 放入 executor options。
3. `sdk/cliproxy/auth/conductor_selection.go` 的 `schedulerOptions` 把 Headers 复制到 `SchedulerPickRequest.Options.Headers`。
4. Scheduler 可以返回：

   ```go
   pluginapi.SchedulerPickResponse{
       AuthID:  targetID,
       Handled: true,
   }
   ```

5. CPA 校验该 AuthID 是否存在于本次 `Candidates`，然后精确选择对应认证。

`host.model.execute` 只跳过调用插件自己的 Router 和 Interceptor，不跳过 Scheduler，所以同一个插件可以发起模型调用并在重入的 `scheduler.pick` 中完成选择。

## token 设计

不能使用进程级 `currentAuthID`。CPA 同时可能处理普通用户流量，该做法会产生串号竞态。

当前实现使用：

- 256-bit `crypto/rand` token。
- 内存映射 `token -> auth ID`。
- 保留头 `X-CPA-Auto-Refresh-Token` 只携带 token，不携带 auth ID。
- 模型调用结束后原子读取选择状态并删除 token。
- 并发安全 registry；不同 token 不会互相覆盖。

Scheduler 行为：

- 没有保留头：`Handled: false`。
- token 有效且目标在 Candidates：返回目标 AuthID，`Handled: true`。
- 保留头为空、重复、未知或过期：错误 envelope。
- token 有效但目标不在 Candidates：错误 envelope。

最后两种情况不能返回 `Handled: false`，也不能返回一个不在 Candidates 中的 AuthID。CPA 对未处理或无效的普通响应可能继续使用内置调度器；错误才会停止这次选择。

## 重试语义

第一次执行失败后，CPA 可能把已经尝试的认证加入 `tried`，后续 Scheduler Candidates 不再包含该 auth ID。插件保留 token 直到整个 `host.model.execute` 返回；如果重试时目标消失，Scheduler 返回错误，阻止 CPA 换用另一认证。

## 原版 ABI 限制

### Home 模式

Home 执行路径在插件 Scheduler 之前分流，因此会绕过 `scheduler.pick`。原版插件 ABI 没有一个无副作用的 Home 状态查询或认证选择探针。本插件不支持 Home 模式。

### 只有一个 Scheduler

CPA 只取优先级最高的一个活动 Scheduler，不会依次调用多个 Scheduler。本插件必须是唯一 Scheduler，或拥有严格最高的插件 priority。普通请求由本插件返回未处理后，会进入 CPA 的内置调度器，而不是另一个插件 Scheduler。

### Model Router 与 Request Interceptor

其他 Model Router 在认证选择前执行，可以把请求路由到插件 Executor，从而完全绕过认证 Scheduler。其他 Request Interceptor 也可以观察或改写私有 token 头。

确定性部署要求：

- 没有处理该模型的其他 Model Router。
- 没有删除或改写 `X-CPA-Auto-Refresh-Token` 的其他 Interceptor。

### auth priority

`availableAuthsForRouteModel` 会先按 auth priority 分组，只把当前最高 priority 档交给插件 Scheduler。低优先级认证即使启用，也不会出现在 Candidates 中。

普通 provider 路由只在该 provider 内分档；mixed-provider 路由会把多个 provider 的候选放在一起取全局最高档。插件无法在执行前可靠判断本次模型是否进入 mixed 路由，所以 0.3.0 使用所有非 disabled 认证记录中的全局最高 priority 作为临时目标值。只对齐到同 provider 的最高值并不足以覆盖 mixed 路由。

`unavailable` 不能安全地从最高值计算中排除：CPA 存在按模型维护 `ModelStates` 的情况，auth 级 `unavailable` 可能只是其他模型失败的聚合状态，而当前目标模型仍然可用。只有 `disabled` / disabled status 能确定不会成为候选。

若 `temporary_priority_override` 保持默认 `false`，低优先级目标仍会失败关闭，并报告 `target auth is unavailable to CPA scheduler`。开启后，插件逐个临时对齐目标 priority，不会改选其他认证。

### 临时 priority 文件事务

原版 ABI 没有“把指定 auth 临时加入 Scheduler Candidates”的接口。`host.auth.save` 虽然能持久化 JSON，但 v7.2.104 的即时 upsert 只把 `priority` 留在 Metadata，没有同步用于调度的 `Attributes["priority"]`；随后 watcher 与 upsert 之间还可能相互覆盖。因此本插件采用 `host.auth.get` + 物理文件 CAS，让 CPA 自己的 watcher 成为运行时更新的唯一来源：

1. `host.auth.get` 返回按 `auth_index` 解析的精确文件路径和原始字节。
2. 插件解析顶层 JSON，保留所有字段的原始 JSON 值，在文件内加入 `_cpa_auto_refresh_quota_priority_override` marker，并把 `priority` 改为全局最高值。
3. 写入前用 SHA-256 比较 `host.auth.get` 时看到的字节和磁盘当前字节；不一致时重新读取并重试，最多三次。
4. 写入使用同目录临时文件、`Sync` 和替换，随后轮询 `host.auth.get_runtime`，直到 CPA runtime 发布预期 priority。
5. 请求结束后重新读取最新文件，只恢复 marker 记录的原始 priority 并删除 marker。这样 token 刷新在请求期间写入的新字段和值会被保留。

marker 同时承担崩溃恢复日志的作用。插件启动和每次任务开始前都会扫描物理认证文件；发现所属 marker 时恢复。该恢复不受当前 `temporary_priority_override` 开关影响。dry-run 不会调用模型，但仍会执行这项遗留 marker 清理，因此不是绝对零写入操作。

没有宿主级 compare-and-swap API，SHA-256 校验与最终替换之间仍存在一个很小的竞态窗口。若其他进程恰好在该窗口写同一文件，理论上仍可能覆盖更新。不要让多个 CPA 进程共享同一 `auth_dir` 并同时启用该功能；进程内锁和 marker 不能提供跨进程事务隔离。

在临时值生效到恢复完成的窗口内，普通 CPA 流量也会看到目标认证处于最高 priority 档，可能选中它。对齐到最高值而不是 `最高值 + 1` 可以保留原最高档中的其他候选，缩小影响，但无法做到完全隔离。应在低流量时段运行，并用 `request_timeout` 限制单个窗口持续时间。

一个物理 JSON 若由插件型 multi-auth parser 展开成多个 `auth_index`，修改文件 priority 会同时影响这些 sibling 记录；这类文件需要额外验证，不应假设每个 auth index 都有独立文件。

### 模型、cooldown 与可用性

Candidates 还会过滤：

- disabled 认证
- 已尝试认证
- 不支持目标模型的认证
- cooldown 或其他当前不可执行认证

插件侧的枚举和运行时复查不能替代宿主最终候选判断，所以 Scheduler 仍必须处理“目标不在 Candidates”的错误分支。

## 私有 Header 边界

原版 Codex 和 Claude 内置 executor 只复制白名单请求头，不会把未知的 `X-CPA-Auto-Refresh-Token` 原样发送到上游。第三方或插件 Executor 可以自行读取或转发 Headers，因此 token 必须短期、随机且不包含 auth ID 或任何秘密。

## Management API

插件只注册受 CPA Management API 保护的路由：

```text
GET  /v0/management/plugins/cpa-auto-refresh-quota/status
POST /v0/management/plugins/cpa-auto-refresh-quota/run
```

不注册匿名 `/v0/resource/plugins/...` 路由。`run` 会产生真实上游请求，必须使用 CPA 的 management key，并建议仅允许 localhost。

## 生命周期

真实的插件配置更新会取消自己的调度器和 active run，然后应用新配置。认证文件 watcher 更新也可能触发同一份配置的 `plugin.reconfigure`；0.3.0 会先做等价比较，同配置重配是 no-op，不能中断正在持有 priority marker 的任务。

启动恢复与正常任务通过同一个进程内优先级锁串行。状态接口暴露 `priority_recovery_pending` 和 `priority_recovery_error`，部署、升级或卸载前必须确认恢复已经结束且没有错误。

原生动态库和宿主回调的运行期卸载行为具有平台差异；特别是 Windows Go DLL 通常不会在进程内真正卸载。因此配置或 DLL 更新后应完整重启 CPA，不依赖热替换。

## 验证清单

插件仓库：

```bash
go test -mod=readonly -count=1 ./...
go vet ./...
go test -mod=readonly -race -count=1 ./...
```

运行验收：

1. 使用原版 CPA v7.2.104。
2. 将动态库放入 `plugins` 目录。
3. 设置 `schedule_enabled: false`，完整启动 CPA。
4. 在 `/v0/management/plugins` 中确认 `scheduler` 与 `management_api` 能力。
5. dry-run 检查候选范围、`priority_override_required`，并确认没有 `priority_recovery_error`。
6. 如需覆盖不同 priority，显式设置 `temporary_priority_override: true`、合理的 `priority_sync_timeout` 和 `request_timeout`。
7. 只允许一个测试 auth index 做第一次真实调用，并检查文件 priority 已恢复、marker 已删除。
8. 从 CPA 请求日志或 usage 记录核对实际认证。
9. 确认没有 Home 模式、其他 Scheduler、冲突 Router/Interceptor，也没有共享同一 `auth_dir` 的第二个 CPA 进程。
10. 再逐步扩大范围并启用自动调度。

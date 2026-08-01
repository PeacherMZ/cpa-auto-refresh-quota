# 贡献指南

感谢你改进本项目。提交 Issue 或 Pull Request 前，请先阅读本文和[安全策略](SECURITY.md)。

## 开始开发

1. Fork 仓库并创建主题分支。
2. 安装 [`go.mod`](go.mod) 指定的 Go 版本和目标平台 C 编译器。
3. 不使用真实 API Key、管理密钥或生产认证文件进行测试。
4. 尽量为行为变更补充自动化测试和中文文档。

推荐分支命名：

```text
feat/简短说明
fix/简短说明
docs/简短说明
```

## 提交前检查

```bash
go fmt ./...
go test -mod=readonly -count=1 ./...
go vet ./...
go test -mod=readonly -race -count=1 ./...
./scripts/check-secrets.sh
git diff --check
```

Windows 可使用：

```powershell
.\scripts\check-secrets.ps1
```

## Pull Request 要求

- 说明问题、解决方式、影响范围和验证结果。
- 行为或配置变更必须同步更新中文文档和示例。
- 不提交 `dist/`、动态库、生成头文件、日志、真实配置或认证数据。
- 不在测试样例中使用看起来像真实凭据的高仿 token。
- 不无关地格式化或重写整个代码库。
- 涉及认证文件写入、调度选择或错误信息时，补充失败路径与并发测试。

## 兼容性变更

涉及 CPA 插件 ABI、Schema、Scheduler、Management API 或认证 callbacks 的变更，需要：

1. 标明验证过的 CPA 版本；
2. 更新 [`docs/INVESTIGATION.md`](docs/INVESTIGATION.md)；
3. 在至少一个目标平台构建动态库；
4. 执行加载、状态查询和 dry-run 验证。

## 提交信息

建议使用清晰的中文提交信息，例如：

```text
修复：恢复优先级时保留最新令牌字段
文档：补充 Linux arm64 构建说明
测试：覆盖并发固定认证选择
```

## 报告安全问题

发现可能泄露密钥、绕过认证选择、任意文件写入或其他安全问题时，请不要创建包含细节的公开 Issue，按[安全策略](SECURITY.md)私下报告。

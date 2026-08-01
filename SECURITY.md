# 安全策略

## 支持范围

安全修复优先应用于最新版本。较旧版本可能只获得升级建议，不保证单独回补。

## 私下报告漏洞

仓库发布到 GitHub 后，请优先使用仓库的 **Security → Advisories → Report a vulnerability** 私下提交报告。不要在公开 Issue、Discussion、Pull Request、日志或截图中披露以下内容：

- API Key、CPA 管理密钥、访问令牌或刷新令牌；
- 真实认证 JSON、`.env`、`config.yaml` 或私钥；
- 可识别用户、服务器、代理地址或内部路径的信息；
- 尚未修复漏洞的完整利用步骤。

报告建议包含：

- 受影响版本和平台；
- 风险类型与可能影响；
- 最小复现步骤；
- 已做脱敏的日志或示例；
- 建议修复方向（如有）。

如果仓库尚未启用私有漏洞报告，请联系维护者索取私下沟通方式，不要先公开漏洞细节。

## 凭据安全

本项目正常开发和测试不需要真实凭据。仓库已忽略常见本地配置、认证目录、私钥和构建产物，但 `.gitignore` 不能替代人工检查。

提交前必须：

```bash
./scripts/check-secrets.sh
git diff --cached
git status --ignored
```

Windows：

```powershell
.\scripts\check-secrets.ps1
git diff --cached
git status --ignored
```

## 如果秘密已经进入 Git 历史

1. 立即撤销或轮换对应秘密，不要只删除文件。
2. 停止推送和发布。
3. 评估秘密是否已被 Fork、缓存、CI 日志或 Release 捕获。
4. 使用适当的历史重写工具清理所有提交和标签。
5. 通知受影响方并重新检查整个历史。

不要把“从最新提交删除文件”误认为已经消除泄露；Git 历史中仍可能保留完整内容。

## 插件运行安全边界

- Management API 应设置强随机密钥，并尽量只监听本机。
- 首次部署保持 `schedule_enabled: false`。
- `temporary_priority_override` 默认关闭；启用前先阅读[配置说明](docs/CONFIGURATION.md)。
- 不要让多个 CPA 进程共享同一认证目录并同时启用临时优先级功能。
- 不要在插件消息、日志或 Issue 中放入秘密。
- 更新或卸载前确认 priority 恢复已完成。

## 依赖与发布

- 依赖版本由 `go.mod` 和 `go.sum` 固定。
- 正式动态库只通过 GitHub Releases 发布，并提供 SHA-256 校验文件。
- 仓库中的 GitHub Actions 会运行测试、静态检查和敏感信息检查。

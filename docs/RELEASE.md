# 发布指南

正式动态库只通过 GitHub Releases 发布，仓库中不得提交 DLL、SO、dylib、生成的 C 头文件或发布压缩包。

## 发布前检查

1. 工作区只包含计划发布的源码和文档变更。
2. `CHANGELOG.md` 已记录新版本内容。
3. README、示例配置和兼容性说明与代码一致。
4. 没有真实配置、认证文件、密钥、日志或抓包内容。
5. 以下命令全部通过：

   ```bash
   go fmt ./...
   go test -mod=readonly -count=1 ./...
   go vet ./...
   go test -mod=readonly -race -count=1 ./...
   ./scripts/check-secrets.sh
   ```

6. 至少在计划发布的平台完成一次加载和 dry-run 验证。

## 版本规则

使用语义化版本，Git 标签格式为 `v主版本.次版本.修订号`，例如：

```text
v0.3.0
```

源码中的 `pluginVersion` 是本地构建默认值。发布工作流会从 Git 标签读取版本，并通过 Go 链接参数注入动态库，避免标签与插件元数据不一致。

## 自动发布流程

推送 `v*` 标签后，[发布工作流](../.github/workflows/release.yml)会：

1. 运行测试、静态检查和敏感信息检查；
2. 构建 Windows amd64 DLL；
3. 构建 Linux amd64 和 arm64 SO；
4. 在 GitHub 提供的 macOS 运行器上构建对应架构 dylib；
5. 按 CPA 的 `plugins/<goos>/<goarch>/` 目录结构打包；
6. 生成 `SHA256SUMS`；
7. 创建 GitHub Release 并上传全部文件。

本地创建标签前建议先查看：

```bash
git status
git diff --check
git tag --list
```

确认后再创建并推送标签：

```bash
git tag -a v0.3.0 -m "发布 v0.3.0"
git push origin v0.3.0
```

## 发布后验证

- 下载每个资产并核对 `SHA256SUMS`。
- 解压后确认目录层级和动态库文件名正确。
- 在对应平台加载插件，执行状态查询和 dry-run。
- 确认 Release 说明包含兼容范围、升级提示和已知限制。

## 回滚

不要覆盖运行中的动态库。回滚时：

1. 禁用自动调度并等待恢复完成；
2. 完整停止 CPA；
3. 把 `plugins.dir` 切换到上一个版本化目录；
4. 完整启动并重新验证。

已发布的有问题版本应在 GitHub Release 中明确标记，不建议静默替换同名资产。

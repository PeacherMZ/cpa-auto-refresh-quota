# 首次创建 GitHub 仓库

当前目录已经初始化为本地 Git 仓库，但尚未创建提交、远程仓库或 GitHub Release。

## 创建远程仓库

在 GitHub 新建空仓库时，不要勾选自动生成 README、`.gitignore` 或许可证，因为本项目已经包含这些文件。

建议仓库名：

```text
cpa-auto-refresh-quota
```

## 检查模块路径

当前 Go module 和插件元数据使用正式仓库地址：

```text
github.com/PeacherMZ/cpa-auto-refresh-quota
```

如果后续迁移到其他账号或组织，应统一替换模块路径、源码 import、README 链接、Issue 安全链接和插件元数据。修改后运行：

```bash
go mod tidy
go test -mod=readonly -count=1 ./...
```

不要只修改 `go.mod`，否则源码中的内部 import 仍会指向旧模块路径。

## 首次提交

先检查工作区：

```bash
git status --ignored
git diff --check
./scripts/check-secrets.sh
```

确认 `dist/`、动态库和本地配置显示为 ignored，且没有任何秘密后，再执行：

```bash
git add .
git commit -m "初始化中文项目仓库"
```

## 关联并推送远程

把占位地址替换为实际仓库：

```bash
git remote add origin https://github.com/你的账号/cpa-auto-refresh-quota.git
git push -u origin main
```

推送前再次确认远程地址：

```bash
git remote -v
```

## 首次发布

源码推送并确认 GitHub Actions 通过后，按[发布指南](RELEASE.md)创建 `v0.3.0` 标签。标签会触发动态库构建和 GitHub Release，不需要把 `dist/` 加入 Git。

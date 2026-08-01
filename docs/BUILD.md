# 构建指南

本项目构建的是 CPA 动态插件，不是 CPA 主程序。正式产物由 GitHub Releases 发布；以下步骤适用于本地验证、二次开发或自行构建。

## 环境要求

- Go 版本以 [`go.mod`](../go.mod) 为准。
- 必须启用 CGO。
- 必须安装目标平台可用的 C 编译器。
- 动态库的操作系统和 CPU 架构必须与运行 CPA 的环境一致。

各系统常见依赖：

- Windows：MSYS2/MinGW-w64 或兼容的 GCC/Clang 工具链。
- Debian/Ubuntu：`sudo apt-get install build-essential`。
- Fedora/RHEL：`sudo dnf groupinstall "Development Tools"`。
- macOS：`xcode-select --install`。

先确认以下命令可用：

```text
go version
go env GOOS GOARCH CGO_ENABLED CC
```

## Windows 构建

在 PowerShell 中执行：

```powershell
.\scripts\build.ps1
```

指定版本和输出目录：

```powershell
.\scripts\build.ps1 -Version 0.3.0 -OutputDir dist
```

生成文件：

```text
dist/cpa-auto-refresh-quota-v0.3.0.dll
```

如果 `go build` 报找不到 C 编译器，请先确保 `gcc.exe` 或兼容编译器位于 `PATH`，再运行：

```powershell
gcc --version
go env CC
```

## Linux 或 macOS 构建

```bash
chmod +x scripts/build.sh
./scripts/build.sh
```

指定版本：

```bash
VERSION=0.3.0 ./scripts/build.sh dist
```

Linux 生成 `.so`，macOS 生成 `.dylib`。

## Docker 构建 Linux 产物

Dockerfile 的最终阶段只包含动态库，不包含 CPA、源码或认证信息。

构建当前架构：

```bash
docker build --target artifact --output type=local,dest=dist/linux .
```

使用 Buildx 构建 Linux amd64：

```bash
docker buildx build \
  --platform linux/amd64 \
  --target artifact \
  --build-arg VERSION=0.3.0 \
  --output type=local,dest=dist/linux-amd64 \
  .
```

构建 Linux arm64：

```bash
docker buildx build \
  --platform linux/arm64 \
  --target artifact \
  --build-arg VERSION=0.3.0 \
  --output type=local,dest=dist/linux-arm64 \
  .
```

非本机架构通常需要 Docker Buildx 与 QEMU 支持。

## 跨系统构建说明

仅设置 `GOOS` 和 `GOARCH` 不足以完成 CGO 跨系统构建，还必须提供目标平台对应的 C 交叉编译器。除非已经正确配置交叉工具链，否则建议：

- Windows DLL 在 Windows 上构建；
- macOS dylib 在 macOS 上构建；
- Linux 多架构产物使用目标机器或 Docker Buildx 构建。

## 本地验证

提交变更前执行：

```bash
go fmt ./...
go test -mod=readonly -count=1 ./...
go vet ./...
go test -mod=readonly -race -count=1 ./...
```

Windows 也可以运行：

```powershell
.\scripts\check-secrets.ps1
```

Linux/macOS：

```bash
./scripts/check-secrets.sh
```

测试和检查不需要真实 API Key、CPA 管理密钥或认证文件。

## 构建产物管理

`dist/`、`.dll`、`.so`、`.dylib` 和 C 头文件已被 `.gitignore` 忽略。不要使用 `git add -f` 强制提交这些文件。正式发布流程见[发布指南](RELEASE.md)。

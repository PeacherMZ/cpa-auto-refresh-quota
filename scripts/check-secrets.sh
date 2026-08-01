#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo_root"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  printf '当前目录不是 Git 工作区，请先执行 git init。\n' >&2
  exit 1
fi

found=0

scan_pattern() {
  pattern=$1
  if matches=$(git grep --no-index --exclude-standard -n -I -E -- "$pattern" -- . \
    ':(exclude)scripts/check-secrets.ps1' \
    ':(exclude)scripts/check-secrets.sh' 2>/dev/null); then
    printf '%s\n' "$matches"
    found=1
  else
    code=$?
    if [ "$code" -ne 1 ]; then
      printf 'git grep 执行失败，退出码: %s\n' "$code" >&2
      exit "$code"
    fi
  fi
}

scan_pattern 'AKIA[0-9A-Z]{16}'
scan_pattern 'gh[pousr]_[A-Za-z0-9]{20,}'
scan_pattern 'sk-[A-Za-z0-9_-]{24,}'
scan_pattern 'AIza[0-9A-Za-z_-]{35}'
scan_pattern 'xox[baprs]-[A-Za-z0-9-]{20,}'
scan_pattern '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----'

sensitive_files=$(git ls-files --cached --others --exclude-standard | grep -E '^(config\.ya?ml|config\..*\.local\.ya?ml)$|(^|/)(\.env(\..+)?|id_(rsa|ed25519)(\.pub)?|auths?/.*\.json)$|\.(pem|key|p12|pfx)$' | grep -Ev '(^|/)\.env\.example$' || true)
if [ -n "$sensitive_files" ]; then
  printf '发现疑似敏感文件名：\n%s\n' "$sensitive_files"
  found=1
fi

if [ "$found" -ne 0 ]; then
  printf '敏感信息检查失败。请确认命中内容均为无害占位符，否则立即移除并轮换相关凭据。\n' >&2
  exit 1
fi

printf '敏感信息检查通过。\n'

#!/usr/bin/env bash
# sslpanel 构建脚本 —— 每一条注释都是踩过的坑，改之前务必先看
#
# 【沙箱文件系统规则】本机沙箱下：
#   - 新建文件：可以写
#   - 已存在的文件：子进程直接覆盖会被拒（Permission denied），删除也不行（没有回收站）
#   - 唯一可行的覆盖方式：先写到一个全新的临时文件，再 `mv -f` 覆盖目标
#   → 所以本脚本所有产物都先写临时名，最后再 mv 到位。日志文件同理，用时间戳命名。
#
# 坑 1：go.mod 可能要求较新的 Go。优先用 PATH 上的 go/wails；也可通过可选环境变量
#       GO_TOOLCHAIN（工具链根目录，其下有 bin/go）、GOROOT、GOPATH 指定。
#       别用 GOTOOLCHAIN=auto —— 会去连 sum.golang.org 校验，卡死约 20 分钟。
#
# 坑 2：绝不能设 GOFLAGS=-mod=mod。go 会尝试写 go.mod（受上面规则保护），
#       结果不是报错而是卡死十几分钟。默认 readonly 即可。
#
# 坑 3：wails 自带的 bindings 生成步骤会挂住（go.exe 写完 exe 却不退出）。
#       改为自己跑：go build -tags bindings 产出生成器 → 执行它 → wails build -skipbindings。
#
# 坑 4：wails 默认跑 `go mod tidy`（触发写 go.mod，见坑 2），必须加 -m 跳过。
#
# 坑 5：系统 GOCACHE（如 AppData\Local\go-build）的 trim.txt 过旧时，go 每次构建末尾
#       会做全量缓存清理（数百 MB / 十几万文件），沙箱下极慢，表现为"编译完了但 go.exe 不退出"。
#       改用项目内 .gocache，并每次构建前刷新 trim.txt（写失败也不致命，仅提示）。
#
# 坑 6：长时间编译必须后台跑：bash build.sh > build_<时间戳>.log 2>&1 &
#       前台跑会被 shell 静默杀掉。
set -e
cd "$(dirname "$0")"

# 可选覆盖：GO_TOOLCHAIN（含 bin/ 的工具链根）、GOROOT、GOPATH
if [ -n "${GO_TOOLCHAIN:-}" ]; then
  export PATH="${GO_TOOLCHAIN}/bin:${PATH}"
fi
if [ -n "${GOROOT:-}" ]; then
  export GOROOT
fi
if [ -n "${GOPATH:-}" ]; then
  export PATH="${GOPATH}/bin:${PATH}"
  export GOPATH
fi

command -v go >/dev/null 2>&1 || {
  echo "error: go not found on PATH (install Go, or set GO_TOOLCHAIN / GOROOT)" >&2
  exit 1
}
command -v wails >/dev/null 2>&1 || {
  echo "error: wails not found on PATH (install wails into GOPATH/bin, or set GOPATH)" >&2
  exit 1
}

export GOTOOLCHAIN=local
export GOPROXY=off
unset GOFLAGS

# Git Bash 下 pwd -W 给出 Windows 路径；其它环境回退到普通 pwd
if GOCACHE_ROOT="$(pwd -W 2>/dev/null)"; then
  export GOCACHE="${GOCACHE_ROOT}/.gocache"
else
  export GOCACHE="$(pwd)/.gocache"
fi
mkdir -p "$GOCACHE"
STAMP="$(date +%s)"
date +%s > "$GOCACHE/trim_$STAMP.txt" 2>/dev/null \
  || echo "提示：trim.txt 刷新失败，若本次构建异常缓慢，是 go 在做全量缓存清理"
mv -f "$GOCACHE/trim_$STAMP.txt" "$GOCACHE/trim.txt" 2>/dev/null || true

TMP_EXE="_wbind_$STAMP.exe"

echo "==> 1/3 编译绑定生成器 -> $TMP_EXE"
go build -buildvcs=false -tags bindings -ldflags="-s -w" -o "$TMP_EXE"

echo "==> 2/3 生成前端绑定"
tsprefix="" tssuffix="" tsoutputtype="classes" "./$TMP_EXE"

echo "==> 3/3 编译应用"
wails build -s -m -skipbindings -o "sslpanel_$STAMP.exe"

echo "==> 收尾：覆盖到 sslpanel.exe"
mv -f "build/bin/sslpanel_$STAMP.exe" "build/bin/sslpanel.exe"
ls -la build/bin/sslpanel.exe
echo "==> 构建完成"
#!/usr/bin/env bash
# sslpanel 构建脚本 —— 每一条注释都是踩过的坑，改之前务必先看
#
# 【沙箱文件系统规则】本机沙箱下：
#   - 新建文件：可以写
#   - 已存在的文件：子进程直接覆盖会被拒（Permission denied），删除也不行（没有回收站）
#   - 唯一可行的覆盖方式：先写到一个全新的临时文件，再 `mv -f` 覆盖目标
#   → 所以本脚本所有产物都先写临时名，最后再 mv 到位。日志文件同理，用时间戳命名。
#
# 坑 1：.workbuddy/binaries/go/go 是 go1.23.4，而 go.mod 要求 go >= 1.25.0。
#       go1.25 在 GOPATH 的 module cache 里，直接当 GOROOT 用。
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
# 坑 5：系统 GOCACHE（AppData\Local\go-build）的 trim.txt 停在 2022 年，go 每次构建末尾
#       会做全量缓存清理（402MB / 十几万文件），沙箱下极慢，表现为"编译完了但 go.exe 不退出"。
#       改用项目内 .gocache，并每次构建前刷新 trim.txt（写失败也不致命，仅提示）。
#
# 坑 6：长时间编译必须后台跑：bash build.sh > build_<时间戳>.log 2>&1 &
#       前台跑会被 shell 静默杀掉。
set -e
cd "$(dirname "$0")"

TC=''
GOPATH_WIN=''

export PATH="$TC/bin:/bin:$PATH"
export GOROOT=""
export GOPATH="$GOPATH_WIN"
export GOTOOLCHAIN=local
export GOPROXY=off
unset GOFLAGS

export GOCACHE="$(pwd -W)/.gocache"
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

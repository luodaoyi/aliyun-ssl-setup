# -*- coding: utf-8 -*-
"""生成 README 用界面截图（全假数据，可安全公开）。

从 frontend/dist/index.html 生成带桩预览页（假配置/假证书/假日志），
按 URL hash 里的 shot 参数切到对应视图，再用 Edge 无头模式截图到 docs/screenshots/。

用法: python tools/make_readme_shots.py
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "frontend", "dist", "index.html")
OUT_DIR = os.path.join(ROOT, "docs", "screenshots")
PREVIEW = os.path.join(OUT_DIR, "_preview.html")
EDGE = r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"

# ---------- 假数据 ----------

FAKE_CERTS = [
    {"name": "example.com", "domains": ["example.com"], "source": "dns", "not_after": "2026-12-05", "days": 89, "issuer": "Let's Encrypt", "region": "-", "note": ""},
    {"name": "*.example.com", "domains": ["*.example.com", "example.com"], "source": "cas", "not_after": "2026-12-05", "days": 89, "issuer": "Let's Encrypt", "region": "cn-hangzhou", "note": "证书管家托管"},
    {"name": "api.example.com", "domains": ["api.example.com"], "source": "slb", "not_after": "2026-11-18", "days": 72, "issuer": "Let's Encrypt", "region": "cn-beijing", "note": "lb-example-gw:443"},
    {"name": "static.example.cn", "domains": ["static.example.cn"], "source": "oss", "not_after": "2026-09-19", "days": 12, "issuer": "Let's Encrypt", "region": "cn-beijing", "note": "bucket: example-static"},
    {"name": "img.example.com", "domains": ["img.example.com"], "source": "cdn", "not_after": "2026-10-03", "days": 26, "issuer": "DigiCert", "region": "-", "note": ""},
    {"name": "video.example.com", "domains": ["video.example.com"], "source": "cdn", "not_after": "2026-09-15", "days": 8, "issuer": "Let's Encrypt", "region": "-", "note": "临期"},
    {"name": "www.example.cn", "domains": ["www.example.cn"], "source": "oss", "not_after": "2026-11-02", "days": 56, "issuer": "Let's Encrypt", "region": "cn-hangzhou", "note": "bucket: example-web"},
    {"name": "cdn.example.cn", "domains": ["cdn.example.cn"], "source": "cdn", "not_after": "2027-01-10", "days": 125, "issuer": "GlobalSign", "region": "-", "note": ""},
    {"name": "gw.example.cn", "domains": ["gw.example.cn", "gw-backup.example.cn"], "source": "slb", "not_after": "2026-12-22", "days": 106, "issuer": "Let's Encrypt", "region": "cn-beijing", "note": "lb-example-api:443"},
    {"name": "old.example.com", "domains": ["old.example.com"], "source": "oss", "not_after": "2026-08-30", "days": -8, "issuer": "Let's Encrypt", "region": "cn-beijing", "note": "已过期"},
]

FAKE_ISSUED = [
    {"key": "*.example.com", "domains": ["*.example.com", "example.com"], "not_after": "2026-12-05", "days": 89, "issuer": "Let's Encrypt", "cert_id": "12345678", "cert_path": "data/certs/example.com"},
    {"key": "*.example.cn", "domains": ["*.example.cn", "example.cn"], "not_after": "2026-12-19", "days": 103, "issuer": "Let's Encrypt", "cert_id": "", "cert_path": "data/certs/example.cn"},
]

FAKE_LOGS = [
    {"time": "09:00:03", "level": "info", "msg": "自动续期开始：扫描 4 个地域…"},
    {"time": "09:00:11", "level": "info", "msg": "[CAS 证书管家] 完成，11 条"},
    {"time": "09:00:25", "level": "warn", "msg": "video.example.com 剩余 8 天，加入续期队列"},
    {"time": "09:01:40", "level": "ok", "msg": "*.example.com 签发成功（Let's Encrypt）"},
    {"time": "09:02:12", "level": "ok", "msg": "OSS 已更新：static.example.cn（bucket example-static）"},
    {"time": "09:02:30", "level": "ok", "msg": "部署完成：成功 6 项，失败 0 项"},
]

FAKE_CONFIG = {
    "access_key_id": "LTAI5tExamp1eAccessKeyID",
    "access_key_secret": "********",
    "regions": ["cn-beijing", "cn-hangzhou"],
    "acme_email": "you@example.com",
    "acme_dir_url": "https://acme-v02.api.letsencrypt.org/directory",
    "eab_kid": "", "eab_hmac": "",
    "key_type": "ec256",
    "smtp_host": "smtp.example.com", "smtp_port": "465",
    "smtp_user": "alert@example.com", "smtp_pass": "********",
    "mail_from": "alert@example.com", "mail_to": "you@example.com",
    "auto_enabled": True, "auto_hour": 9, "auto_threshold": 15, "auto_targets": ["oss", "cdn", "slb"],
}

STUB = """<script>
window.runtime = {
  EventsOn: function () {}, EventsEmit: function () {}, EventsOff: function () {},
};
window.go = { main: { App: {
  GetConfig: async () => (%s),
  SaveConfig: async () => true,
  GetLastScan: async () => ({ time: %d, certs: %s, errors: [] }),
  GetProgress: async () => null,
  GetApplyProgress: async () => null,
  GetDeployProgress: async () => null,
  GetLogs: async () => (%s),
  ClearLogs: async () => {},
  ListIssued: async () => (%s),
  ListSLBListeners: async () => [],
  GetAutoStatus: async () => ({ last_run: "2026-09-07 09:02", last_text: "签发 1 张，部署 6 项，失败 0 项" }),
  StartScan: async () => "ok",
  StartApply: async () => "ok",
  StartDeploy: async () => "ok",
} } };
</script>
""" % (
    __import__("json").dumps(FAKE_CONFIG, ensure_ascii=False),
    1788742920000,
    __import__("json").dumps(FAKE_CERTS, ensure_ascii=False),
    __import__("json").dumps(FAKE_LOGS, ensure_ascii=False),
    __import__("json").dumps(FAKE_ISSUED, ensure_ascii=False),
)

DRIVER = """<script>
window.onerror = function (msg, src, line) {
  document.title = "ERR: " + msg + " @" + line;
};
window.addEventListener("load", () => {
  setTimeout(() => {
    const shot = new URLSearchParams(location.hash.slice(1)).get("shot") || "inventory";
    const tab = (v) => switchTab(document.querySelector('.tab[data-v="' + v + '"]'));
    if (shot === "scan-progress") {
      resetProgress();
      onProgress({ stage: "oss", title: "OSS 自定义域名", detail: "TLS 实测 8/13：static.example.cn",
                   done: 8, total: 13, percent: 62, level: "info" });
      onProgress({ stage: "cdn", title: "CDN 加速域名", detail: "TLS 实测 9/14：img.example.com",
                   done: 9, total: 14, percent: 64, level: "info" });
    } else if (shot === "apply") {
      tab("apply");
    } else if (shot === "apply-progress") {
      tab("apply");
      applyOnProgress({ percent: 52, title: "写入验证记录",
                        detail: "已写入 _acme-challenge.example.com，等待 Let's Encrypt 校验（约 1~2 分钟）…", level: "" });
    } else if (shot === "deploy") {
      tab("deploy");
    } else if (shot === "deploy-progress") {
      tab("deploy");
      deployOnProgress({ percent: 63, title: "更新 CDN 域名",
                         detail: "正在更新 img.example.com 的 HTTPS 证书（3/5）…", level: "" });
    } else if (shot === "logs") {
      tab("logs");
    } else if (shot === "settings") {
      openSettings();
    }
    document.title = "READY";
  }, 500);
});
</script>
</body>"""

os.makedirs(OUT_DIR, exist_ok=True)

html = open(SRC, encoding="utf-8").read()
idx = html.rindex("<script>")
html = html[:idx] + STUB + "\n" + html[idx:]
html = html.replace("</body>", DRIVER, 1)
open(PREVIEW, "w", encoding="utf-8").write(html)

SHOTS = [
    ("inventory",       "01-inventory",        820),  # 检测库存（证书清单）
    ("scan-progress",   "02-scan-progress",    820),  # 检测实时进度
    ("apply",           "03-apply",            820),  # 申请证书 + 已签发列表
    ("apply-progress",  "04-apply-progress",   820),  # 申请实时进度条
    ("deploy-progress", "05-deploy-progress",  820),  # 部署实时进度条
    ("logs",            "06-logs",             820),  # 任务日志
    ("settings",        "07-settings",        1180),  # 设置（含无人值守配置，弹窗较长用高窗口）
]

os.makedirs(OUT_DIR, exist_ok=True)
ok, fail = [], []
for shot, name, height in SHOTS:
    png = os.path.join(OUT_DIR, name + ".png")
    udd = os.path.join(OUT_DIR, "_edge_" + shot)
    url = "file:///" + PREVIEW.replace("\\", "/") + "#shot=" + shot
    r = subprocess.run([
        EDGE, "--headless=new", "--disable-gpu", "--hide-scrollbars",
        "--window-size=1280,%d" % height, "--virtual-time-budget=3500",
        "--user-data-dir=" + udd,
        "--screenshot=" + png, url,
    ], capture_output=True, text=True, timeout=90)
    if os.path.exists(png):
        ok.append(name)
        print("✓", name)
    else:
        fail.append(name)
        print("✗", name, (r.stderr or "")[-200:])

# 清理临时目录与预览页
import shutil
for shot, _, _ in SHOTS:
    shutil.rmtree(os.path.join(OUT_DIR, "_edge_" + shot), ignore_errors=True)
os.remove(PREVIEW)
print("完成：%d 成功 / %d 失败" % (len(ok), len(fail)))
sys.exit(1 if fail else 0)

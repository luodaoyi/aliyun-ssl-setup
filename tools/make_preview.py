# -*- coding: utf-8 -*-
"""从真实的 index.html 生成「检测进度」预览页。

复用页面自身的 CSS 与 JS（resetProgress / onProgress / renderStages 都是页面里的真实函数），
只把 wails 的桥接(window.go / window.runtime)桩掉，再回放一次真实的进度事件流，
这样在没有 WebView2 的环境里也能看到进度面板的真实渲染效果。

用法: python tools/make_preview.py
输出: progress-preview.html
"""
import re

SRC = "frontend/dist/index.html"
OUT = "progress-preview.html"

# 真实抓到的进度事件流（sslpanel-app 对阿里云只读 API 的一次完整扫描，11.4s / 116 条证书）
EVENTS = [
    (0.00, "init", "准备", "开始扫描 5 类云资源…", 0, 0, "info"),
    (0.00, "dns", "云解析域名", "正在查询云解析域名…", 0, 0, "info"),
    (0.00, "oss", "OSS 自定义域名", "正在列出 Bucket…", 0, 0, "info"),
    (0.00, "cdn", "CDN 加速域名", "正在查询加速域名列表…", 0, 0, "info"),
    (0.00, "slb", "SLB 负载均衡", "正在查询 2 个地域…", 0, 0, "info"),
    (0.00, "cas", "CAS 证书管家", "正在查询证书列表…", 0, 0, "info"),
    (0.02, "slb", "SLB 负载均衡", "查询 cn-beijing（1/2）…", 0, 2, "info"),
    (0.17, "dns", "云解析域名", "完成，8 个域名", 8, 8, "ok"),
    (0.18, "dns", "云解析域名", "完成，8 条", 8, 8, "ok"),
    (0.26, "oss", "OSS 自定义域名", "发现 38 个 Bucket，查询绑定域名…", 0, 38, "info"),
    (0.29, "slb", "SLB 负载均衡", "cn-beijing 完成，7 张证书", 1, 2, "ok"),
    (0.30, "slb", "SLB 负载均衡", "查询 cn-hangzhou（2/2）…", 1, 2, "info"),
    (0.33, "cas", "CAS 证书管家", "返回 11 张证书，解析中…", 0, 11, "info"),
    (0.34, "cas", "CAS 证书管家", "解析证书 5/11：cert-4msyd7", 5, 11, "info"),
    (0.36, "cas", "CAS 证书管家", "解析证书 10/11：cert-15559237", 10, 11, "info"),
    (0.38, "cas", "CAS 证书管家", "解析证书 11/11：cert-15559236", 11, 11, "info"),
    (0.40, "cas", "CAS 证书管家", "完成，11 条", 11, 11, "ok"),
    (0.45, "cdn", "CDN 加速域名", "发现 14 个加速域名，开始 TLS 实测…", 0, 14, "info"),
    (0.52, "oss", "OSS 自定义域名", "查询绑定域名 5/38：admin-sunny-prod", 5, 38, "info"),
    (0.60, "cdn", "CDN 加速域名", "TLS 实测 5/14：oss.sqyouxiang.com", 5, 14, "info"),
    (0.72, "slb", "SLB 负载均衡", "cn-hangzhou 完成，0 张证书", 2, 2, "ok"),
    (0.74, "slb", "SLB 负载均衡", "完成，7 条", 7, 7, "ok"),
    (0.80, "oss", "OSS 自定义域名", "查询绑定域名 10/38：ddyx-test-erp", 10, 38, "info"),
    (0.90, "cdn", "CDN 加速域名", "TLS 实测 10/14：test-admin.sqyouxiang.com", 10, 14, "info"),
    (0.98, "oss", "OSS 自定义域名", "查询绑定域名 15/38：syc-cms-prod", 15, 38, "info"),
    (1.10, "oss", "OSS 自定义域名", "查询绑定域名 20/38：webplus-cn-beijing-…", 20, 38, "info"),
    (1.22, "oss", "OSS 自定义域名", "查询绑定域名 25/38：jobsmart-doc", 25, 38, "info"),
    (1.34, "cdn", "CDN 加速域名", "TLS 实测 14/14：admin.sunnykids.cc", 14, 14, "info"),
    (1.40, "cdn", "CDN 加速域名", "完成，14 条", 14, 14, "ok"),
    (1.46, "oss", "OSS 自定义域名", "查询绑定域名 30/38：smartfactory-doc", 30, 38, "info"),
    (1.58, "oss", "OSS 自定义域名", "查询绑定域名 35/38：zhihuiyan-open-test", 35, 38, "info"),
    (1.70, "oss", "OSS 自定义域名", "查询绑定域名 38/38：smartfactory-erp-prod", 38, 38, "info"),
    (1.80, "oss", "OSS 自定义域名", "发现 76 个域名，开始 TLS 实测证书…", 0, 13, "info"),
    (2.10, "oss", "OSS 自定义域名", "TLS 实测 5/13：test-erp.iforge.cn", 5, 13, "info"),
    (2.60, "oss", "OSS 自定义域名", "TLS 实测 10/13：oss-supervise.jlscyw.org", 10, 13, "info"),
    (3.40, "oss", "OSS 自定义域名", "TLS 实测 13/13：sunnykids.cc", 13, 13, "info"),
    (3.60, "oss", "OSS 自定义域名", "完成，76 个域名", 76, 76, "ok"),
    (3.70, "oss", "OSS 自定义域名", "完成，76 条", 76, 76, "ok"),
    (3.90, "done", "汇总", "扫描完成：116 条证书", 116, 116, "ok"),
]

STUB = """<script>
/* ===== 预览桩：把 wails 的桥接替换掉，页面其余 CSS/JS 全部保持原样 ===== */
window.__handlers = {};
window.runtime = {
  EventsOn: function (name, fn) { (window.__handlers[name] = window.__handlers[name] || []).push(fn); },
  EventsEmit: function () {},
  EventsOff: function () {},
};
window.go = { main: { App: {
  GetConfig: async () => ({ access_key_id: "LTAI****BVQ", access_key_secret: "***",
    regions: ["cn-beijing", "cn-hangzhou"], acme_dir_url: "https://acme.zerossl.com/v2/DV90" }),
  SaveConfig: async () => true,
  GetLastScan: async () => null,
  StartScan: async () => "ok",
  GetProgress: async () => null,
  GetLogs: async () => [],
  ClearLogs: async () => {},
  ListIssued: async () => [],
  ListSLBListeners: async () => [],
  StartApply: async () => "ok",
  StartDeploy: async () => "ok",
} } };
</script>
"""

DRIVER = """<script>
/* ===== 回放真实进度事件流（数据来自一次真实扫描：11.4s / 116 条证书） ===== */
const EVENTS = %s;

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function replay() {
  scanning = true;
  resetProgress();
  document.getElementById("scanBtn").disabled = true;
  document.getElementById("scanBtn").innerHTML = '<span class="spin"></span>检测中…';
  document.getElementById("empty").style.display = "none";
  const t0 = performance.now();
  for (const e of EVENTS) {
    const wait = e.t * 1000 - (performance.now() - t0);
    if (wait > 0) await sleep(wait);
    onProgress({ stage: e.stage, title: e.title, detail: e.detail,
                 done: e.done, total: e.total, percent: e.total ? Math.round(e.done * 100 / e.total) : 0,
                 level: e.level });
  }
  const btn = document.getElementById("scanBtn");
  btn.disabled = false;
  btn.textContent = "重放一次";
}

window.addEventListener("load", () => {
  const btn = document.getElementById("scanBtn");
  btn.addEventListener("click", (ev) => { ev.stopPropagation(); replay(); }, true);
  setTimeout(replay, 300);
});
</script>
</body>"""

import json

ev_json = json.dumps(
    [{"t": t, "stage": s, "title": ti, "detail": d, "done": dn, "total": tt, "level": lv}
     for (t, s, ti, d, dn, tt, lv) in EVENTS],
    ensure_ascii=False, indent=2)

html = open(SRC, encoding="utf-8").read()

# 1) 在页面主脚本之前插入桥接桩
idx = html.rindex("<script>")
html = html[:idx] + STUB + "\n" + html[idx:]

# 2) 在 </body> 之前插入回放驱动
html = html.replace("</body>", (DRIVER % ev_json), 1)

# 3) 页面里的 startScan 会走真实桥接，这里改成空实现，避免和回放冲突
html = html.replace("onclick=\"startScan()\"", "onclick=\"replay()\"", 1)

open(OUT, "w", encoding="utf-8").write(html)
print("生成", OUT, len(html), "字节，事件", len(EVENTS), "条")

"""只读探测：拉取 OSS Bucket 的 CNAME 配置原始 XML，确认证书节点结构。"""
import json
import sys
import hmac
import hashlib
import base64
import urllib.request
from datetime import datetime, timezone

# 与 Wails 打包 exe 同目录：仓库内为 build/bin/config.json
cfg = json.load(open("build/bin/config.json", encoding="utf-8"))
AK, SK = cfg["access_key_id"], cfg["access_key_secret"]


def oss_get(bucket, loc, domain=""):
    host = f"{bucket}.{loc}.aliyuncs.com"
    canon = f"/{bucket}/?cname"
    date = datetime.now(timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT")
    sts = f"GET\n\n\n{date}\n{canon}"
    sig = base64.b64encode(hmac.new(SK.encode(), sts.encode(), hashlib.sha1).digest()).decode()
    req = urllib.request.Request(f"https://{host}/?cname", headers={
        "Date": date,
        "Authorization": f"OSS {AK}:{sig}",
    })
    with urllib.request.urlopen(req, timeout=20) as r:
        return r.read().decode("utf-8")


bucket, loc = sys.argv[1], sys.argv[2]
want = sys.argv[3] if len(sys.argv) > 3 else ""
xml = oss_get(bucket, loc)
print(f"=== {bucket} ({loc}) ===")
if want:
    # 只打印包含目标域名的 Cname 块
    for blk in xml.split("<Cname>")[1:]:
        if f"<Domain>{want}</Domain>" in blk:
            print("<Cname>" + blk)
else:
    print(xml[:3000])
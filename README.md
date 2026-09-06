# aliyun-ssl-setup

阿里云 SSL 证书桌面面板（Windows 单二进制，基于 Wails v2 + Go）。

覆盖证书全生命周期：**检测 → 申请 → 部署 → 续期提醒**。

- **检测**：扫描 CAS（证书管家）/ SLB / OSS / CDN / 云解析 DNS，汇总所有证书的有效期与绑定关系
- **申请**：lego + ACME DNS-01（支持 Let's Encrypt / ZeroSSL），自动写 TXT 验证记录，支持通配符与多域名 SAN
- **部署**：一键部署到 OSS 自定义域名 / CDN 加速域名 / SLB HTTPS 监听，部署后自动刷新检测结果

## 编译

依赖：Go ≥ 1.25、Wails v2。Windows 下用 PowerShell 或 Git Bash 执行：

```bash
bash build.sh
```

产物：`build/bin/sslpanel.exe`（约 13 MB，免安装，无需浏览器）。

## AccessKey 配置

### 1. 配置文件位置

面板从 **exe 同目录**的 `config.json` 读取配置，**仅在启动时读取一次**——修改配置后必须重启面板。

首次运行前，在 `build/bin/` 下创建 `config.json`：

```json
{
  "access_key_id": "<你的 AccessKey ID>",
  "access_key_secret": "<你的 AccessKey Secret>",
  "regions": ["cn-beijing", "cn-hangzhou"],
  "acme_email": "you@example.com",
  "acme_dir_url": "https://acme-v02.api.letsencrypt.org/directory",
  "eab_kid": "",
  "eab_hmac": "",
  "key_type": "ec256"
}
```

> ⚠️ `config.json` 含真实凭证，已被 `.gitignore` 排除，**严禁提交到仓库**。

### 2. 字段说明

| 字段 | 必填 | 说明 |
|------|:---:|------|
| `access_key_id` / `access_key_secret` | ✅ | 阿里云 AK 对，检测 + 申请 + 部署共用 |
| `regions` | ✅ | 扫描的地域列表，如 `cn-beijing`、`cn-hangzhou` |
| `acme_email` | ✅（申请时） | ACME 账号邮箱，CA 到期通知发到该邮箱 |
| `acme_dir_url` | ❌ | ACME 目录地址。默认 ZeroSSL；用 Let's Encrypt 填 `https://acme-v02.api.letsencrypt.org/directory` |
| `eab_kid` / `eab_hmac` | 视 CA | ZeroSSL 强制 EAB（控制台 Developer 页生成）；**Let's Encrypt 留空即可** |
| `key_type` | ❌ | 证书密钥类型：`ec256`（默认推荐）/ `rsa2048` / `rsa4096` |

### 3. RAM 权限要求

建议 RAM 用户 + 最小权限，**不要用主账号 AK**。面板需要两类权限：

**① 只读权限（检测功能）**：挂系统策略 `AliyunReadOnlyAccess`。

**② 写权限（申请 + 部署功能）**：自定义策略，最小 Action 集如下（完整可复制版本见 [`docs/ram-policy-ssl-deploy.json`](docs/ram-policy-ssl-deploy.json)）：

| Action | 用途 |
|--------|------|
| `alidns:AddDomainRecord` / `DeleteDomainRecord` | ACME DNS-01 验证（写/删 TXT 记录） |
| `yundun-cert:UploadUserCertificate` | 上传证书到证书管家（注意前缀是 **`yundun-cert:`**，不是 `cas:`） |
| `oss:PutCname` | OSS 自定义域名挂证书 |
| `cdn:SetCdnDomainSSLCertificate` | CDN 域名挂证书 |
| `slb:UploadServerCertificate` / `SetLoadBalancerHTTPSListenerAttribute` | SLB 挂证书 / 更新 HTTPS 监听 |

> 已知坑：CAS 新版 API（2020-04-07）的 RamCode 是 `yundun-cert`，策略里写 `cas:UploadUserCertificate` 不生效，会报 `NoPermission`。

### 4. 权限自检方法

无需真实写操作即可验证 AK 权限——用**无效参数**调用写 API：权限不足会报 `Forbidden`/`NoPermission`，权限正常则报业务错误（如"域名不存在"）：

```bash
aliyun alidns AddDomainRecord --DomainName "test.invalid" --RR t --Type TXT --Value v
aliyun cas UploadUserCertificate --Name "t"
aliyun cdn SetCdnDomainSSLCertificate --DomainName "test.invalid" --SSLProtocol on
aliyun slb UploadServerCertificate --RegionId cn-beijing
```

## 日常维护

### AK 轮换

1. RAM 控制台为对应用户创建新 AccessKey（保留旧的）
2. 更新 `config.json` 中的 AK → 重启面板 → 点「立即检测」确认 116+ 条扫描正常
3. 确认无误后禁用/删除旧 AK
4. 若同 AK 还用于其他系统（如每日巡检脚本），同步更新各处配置后再删旧 AK

建议每 6~12 个月轮换一次；AK 泄露（误提交 git、截图露出等）立即轮换。

### 证书续期

- 证书 90 天有效期，面板检测页按剩余天数排序，剩余 < 30 天标黄、< 7 天标红
- 续期 = 面板里重新「申请」+「部署」，账号密钥（`account.key`）自动复用，无需重新注册
- Let's Encrypt 有速率限制：同一域名每周 5 张重复证书，正常续期足够

### 故障排查

| 现象 | 排查 |
|------|------|
| 窗口白屏 / 闪退 | 多显卡或远控（ToDesk 等）环境导致 WebView2 GPU 崩溃，本仓库 `main.go` 已内置 `WebviewGpuIsDisabled` 修复；若自行改造请保留 |
| 检测报权限错误 | AK 被改过权限，按上文重新自检 |
| 申请报 "ACME 账号注册失败" | ZeroSSL 需填 EAB；或改用 Let's Encrypt（`acme_dir_url` 换成 LE 地址、EAB 留空） |
| 部署报 NoPermission | 对照第 3 节权限表，重点检查 `yundun-cert:` 前缀 |
| 改了 config.json 不生效 | 重启面板（启动时读取一次） |

### 目录结构

```
sslpanel-app/
├── main.go                 # Wails 入口（含 WebView2 崩溃修复开关）
├── app.go                  # 配置加载 / 任务编排 / 事件推送
├── internal/
│   ├── aliyun/             # 阿里云 RPC 客户端 + 资源扫描
│   ├── acme/               # ACME 申请（lego）+ 阿里云 DNS Provider
│   ├── deploy/             # OSS / CDN / SLB 部署
│   └── store/              # 任务记录存储
├── frontend/dist/          # 前端单文件（无 npm 依赖）
├── tools/probe/            # 无头进度探针（验证后端扫描链路）
├── docs/                   # RAM 权限策略等文档
└── build.sh                # 一键编译脚本
```

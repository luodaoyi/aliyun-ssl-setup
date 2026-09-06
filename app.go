package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sslpanel-app/internal/acme"
	"sslpanel-app/internal/aliyun"
	"sslpanel-app/internal/deploy"
	"sslpanel-app/internal/notify"
	"sslpanel-app/internal/store"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// AppConfig 本地配置（存在 exe 同目录 config.json）
type AppConfig struct {
	AccessKeyID     string   `json:"access_key_id"`
	AccessKeySecret string   `json:"access_key_secret"`
	Regions         []string `json:"regions"`

	// ACME 申请
	ACMEEmail  string `json:"acme_email"`
	ACMEDirURL string `json:"acme_dir_url"` // 默认 ZeroSSL
	EABKid     string `json:"eab_kid"`
	EABHmac    string `json:"eab_hmac"`
	KeyType    string `json:"key_type"` // rsa2048 / ec256 等

	// 邮件告警（续期/部署失败）
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	MailTo   string `json:"mail_to"`
	MailFrom string `json:"mail_from"`

	// 无人值守自动续期（面板内定时器，需保持面板运行）
	AutoEnabled   bool     `json:"auto_enabled"`   // 是否开启每日自动续期
	AutoHour      int      `json:"auto_hour"`      // 每天几点执行（0-23，默认 9）
	AutoThreshold int      `json:"auto_threshold"` // 剩余天数阈值（默认 15 天）
	AutoTargets   []string `json:"auto_targets"`   // 允许自动替换的服务：oss / cdn / slb
}

// TaskLog 一行任务日志（推送给前端）
type TaskLog struct {
	Time  string `json:"time"`
	Level string `json:"level"` // info / ok / warn / err
	Msg   string `json:"msg"`
}

// App 桌面应用后端，方法绑定到前端 window.go.main.App.*
type App struct {
	ctx     context.Context
	cfg     AppConfig
	cfgPath string
	dataDir string
	certDir string

	mu       sync.Mutex
	scanning bool
	busy     bool
	result   *store.ScanResult
	issued   []store.IssuedCert
	logs     []TaskLog
	progress *aliyun.Progress

	applyProgress *aliyun.Progress // 证书申请实时进度

	deployProgress *aliyun.Progress // 证书部署实时进度

	lastAutoRun  string // 上次自动续期执行时间
	lastAutoText string // 上次自动续期结果摘要
}

func NewApp() *App {
	a := &App{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		a.cfgPath = filepath.Join(dir, "config.json")
		a.dataDir = filepath.Join(dir, "data")
	}
	if a.cfgPath == "" {
		a.cfgPath = "config.json"
		a.dataDir = "data"
	}
	a.certDir = filepath.Join(a.dataDir, "certs")
	a.loadConfig()
	a.loadIssued()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.startAutoLoop()
}

// ---------- 日志 ----------

func (a *App) log(level, format string, args ...any) {
	entry := TaskLog{
		Time:  time.Now().Format("15:04:05"),
		Level: level,
		Msg:   fmt.Sprintf(format, args...),
	}
	a.mu.Lock()
	a.logs = append(a.logs, entry)
	if len(a.logs) > 800 {
		a.logs = a.logs[len(a.logs)-800:]
	}
	a.mu.Unlock()
	wruntime.EventsEmit(a.ctx, "task:log", entry)
}

// GetLogs 返回当前任务日志
func (a *App) GetLogs() []TaskLog {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TaskLog, len(a.logs))
	copy(out, a.logs)
	return out
}

// ClearLogs 清空日志
func (a *App) ClearLogs() {
	a.mu.Lock()
	a.logs = nil
	a.mu.Unlock()
}

// ---------- 配置 ----------

func (a *App) loadConfig() {
	a.cfg = AppConfig{Regions: []string{"cn-beijing", "cn-hangzhou"}}
	data, err := os.ReadFile(a.cfgPath)
	if err != nil {
		return
	}
	var c AppConfig
	if json.Unmarshal(data, &c) == nil {
		if len(c.Regions) == 0 {
			c.Regions = []string{"cn-beijing", "cn-hangzhou"}
		}
		// 自动续期默认值：每天 9 点，剩余 15 天触发，覆盖三类服务
		if c.AutoHour == 0 {
			c.AutoHour = 9
		}
		if c.AutoThreshold == 0 {
			c.AutoThreshold = 15
		}
		if len(c.AutoTargets) == 0 {
			c.AutoTargets = []string{"oss", "cdn", "slb"}
		}
		a.cfg = c
	}
}

// GetConfig 返回当前配置（本机桌面应用，明文返回便于回填编辑）
func (a *App) GetConfig() AppConfig {
	return a.cfg
}

// SaveConfig 保存配置
func (a *App) SaveConfig(c AppConfig) error {
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.AccessKeySecret = strings.TrimSpace(c.AccessKeySecret)
	if c.AccessKeyID == "" || c.AccessKeySecret == "" {
		return fmt.Errorf("AccessKey ID 和 Secret 不能为空")
	}
	if len(c.Regions) == 0 {
		c.Regions = []string{"cn-beijing", "cn-hangzhou"}
	}
	a.cfg = c
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.cfgPath, data, 0o600)
}

// ImportAliyunConfig 从本机 aliyun CLI 默认配置导入 AK
func (a *App) ImportAliyunConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".aliyun", "config.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", p, err)
	}
	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("解析失败: %w", err)
	}
	ak, sk := "", ""
	if prof, ok := d["profiles"].([]any); ok && len(prof) > 0 {
		if m, ok := prof[0].(map[string]any); ok {
			ak = str(m["access_key_id"], m["accessKeyId"])
			sk = str(m["access_key_secret"], m["accessKeySecret"])
		}
	} else {
		ak = str(d["access_key_id"], d["accessKeyId"])
		sk = str(d["access_key_secret"], d["accessKeySecret"])
	}
	if ak == "" || sk == "" {
		return "", fmt.Errorf("配置文件中没有找到 AccessKey")
	}
	a.cfg.AccessKeyID = ak
	a.cfg.AccessKeySecret = sk
	if err := a.SaveConfig(a.cfg); err != nil {
		return "", err
	}
	return fmt.Sprintf("已导入 %s 下的 AccessKey（%s…）", p, ak[:8]), nil
}

func str(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// ---------- 扫描（检测） ----------

// GetLastScan 返回上次扫描结果（含启动时从磁盘缓存的）
func (a *App) GetLastScan() *store.ScanResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.result != nil {
		return a.result
	}
	if st, err := store.New(a.dataDir); err == nil {
		a.result = st.Get()
		return a.result
	}
	return nil
}

// IsScanning 是否正在扫描
func (a *App) IsScanning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scanning
}

// GetProgress 返回当前扫描进度（供前端切回页面时补齐状态）
func (a *App) GetProgress() *aliyun.Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.progress
}

// GetApplyProgress 返回当前证书申请进度（供前端切回页面时补齐状态）
func (a *App) GetApplyProgress() *aliyun.Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.applyProgress
}

// GetDeployProgress 返回当前证书部署进度（供前端切回页面时补齐状态）
func (a *App) GetDeployProgress() *aliyun.Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deployProgress
}

// StartScan 启动一次扫描（异步），过程中推送 scan:progress，完成后推送 scan:done
func (a *App) StartScan() (string, error) {
	if a.cfg.AccessKeyID == "" || a.cfg.AccessKeySecret == "" {
		return "", fmt.Errorf("尚未配置 AccessKey，请先到「设置」填写或导入")
	}
	a.mu.Lock()
	if a.scanning {
		a.mu.Unlock()
		return "already", nil
	}
	a.scanning = true
	a.progress = &aliyun.Progress{
		Stage: "init", Title: "准备", Detail: "正在启动…", Level: "info",
	}
	a.mu.Unlock()

	go func() {
		res := a.doScan()
		final := aliyun.Progress{
			Stage: "done", Title: "汇总", Percent: 100,
			Done: 5, Total: 5,
		}
		if res != nil {
			final.Detail = fmt.Sprintf("扫描完成，共 %d 条", len(res.Certs))
		} else {
			final.Detail = "扫描失败"
		}
		final.Level = "ok"
		a.mu.Lock()
		a.scanning = false
		if res != nil {
			a.result = res
		}
		a.progress = &final
		a.mu.Unlock()
		wruntime.EventsEmit(a.ctx, "scan:progress", final)
		wruntime.EventsEmit(a.ctx, "scan:done", res)
	}()
	return "started", nil
}

// doScan 同步执行一次扫描并写回缓存（供手动扫描与自动续期共用）
func (a *App) doScan() *store.ScanResult {
	cl := aliyun.NewClient(a.cfg.AccessKeyID, a.cfg.AccessKeySecret)
	sc := aliyun.NewScanner(cl, a.cfg.Regions)
	sc.OnProgress = func(p aliyun.Progress) {
		cp := p
		a.mu.Lock()
		a.progress = &cp
		a.mu.Unlock()
		wruntime.EventsEmit(a.ctx, "scan:progress", cp)
		// 同时落一行任务日志，方便事后回看
		level := p.Level
		if level == "" {
			level = "info"
		}
		a.log(level, "[%s] %s", p.Title, p.Detail)
	}
	entries, errs := sc.ScanAll()
	res := &store.ScanResult{
		Time:   time.Now(),
		Certs:  aliyun.TrimDup(entries),
		Errors: errs,
	}
	a.mu.Lock()
	a.result = res
	a.mu.Unlock()
	if st, err := store.New(a.dataDir); err == nil {
		st.Set(res)
	}
	if len(errs) > 0 {
		a.log("warn", "共 %d 项子任务报错，详见上方记录", len(errs))
	}
	return res
}

// ---------- 申请 ----------

func (a *App) loadIssued() {
	data, err := os.ReadFile(filepath.Join(a.dataDir, "issued.json"))
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &a.issued)
}

func (a *App) saveIssued() {
	_ = os.MkdirAll(a.dataDir, 0o755)
	data, _ := json.MarshalIndent(a.issued, "", "  ")
	_ = os.WriteFile(filepath.Join(a.dataDir, "issued.json"), data, 0o600)
}

// ListIssued 列出本地已签发的证书
func (a *App) ListIssued() []store.IssuedCert {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]store.IssuedCert, len(a.issued))
	copy(out, a.issued)
	return out
}

// UploadCertToAliyun 上传本地证书到阿里云证书管家（CAS），返回 CertID。
// 已上传过（CertID 非空）直接返回 "already:<id>"，不重复上传。
func (a *App) UploadCertToAliyun(key string) (string, error) {
	if a.cfg.AccessKeyID == "" || a.cfg.AccessKeySecret == "" {
		return "", fmt.Errorf("尚未配置 AccessKey")
	}
	a.mu.Lock()
	var rec *store.IssuedCert
	for i := range a.issued {
		if a.issued[i].Key == key {
			rec = &a.issued[i]
			break
		}
	}
	a.mu.Unlock()
	if rec == nil {
		return "", fmt.Errorf("未找到本地证书 %s", key)
	}
	if rec.CertID != "" {
		return "already:" + rec.CertID, nil
	}

	certPEM, err := os.ReadFile(rec.CertPath)
	if err != nil {
		return "", fmt.Errorf("读取证书文件失败: %w", err)
	}
	keyPEM, err := os.ReadFile(rec.KeyPath)
	if err != nil {
		return "", fmt.Errorf("读取私钥文件失败: %w", err)
	}

	name := "sslpanel-" + key
	if rec.NotAfter != "" {
		name += "-" + strings.ReplaceAll(rec.NotAfter, "-", "")
	}
	cl := aliyun.NewClient(a.cfg.AccessKeyID, a.cfg.AccessKeySecret)
	id, err := deploy.UploadToCAS(cl, deploy.Cert{Name: name, CertPEM: string(certPEM), KeyPEM: string(keyPEM)})
	if err != nil {
		return "", err
	}
	a.log("ok", "已上传到阿里云证书管家：%s（ID %s）", name, id)

	a.mu.Lock()
	for i := range a.issued {
		if a.issued[i].Key == key {
			a.issued[i].CertID = id
			break
		}
	}
	a.mu.Unlock()
	a.saveIssued()
	return id, nil
}

// IsBusy 是否有申请/部署任务在跑
func (a *App) IsBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

func (a *App) tryBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return false
	}
	a.busy = true
	return true
}

// StartApply 申请证书（异步 DNS-01），完成后推送 apply:done
func (a *App) StartApply(domains []string) (string, error) {
	clean := []string{}
	for _, d := range domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			clean = append(clean, d)
		}
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("请至少填写一个域名")
	}
	if strings.TrimSpace(a.cfg.ACMEEmail) == "" {
		return "", fmt.Errorf("请先在「设置」填写 ACME 账号邮箱")
	}
	if a.cfg.AccessKeyID == "" {
		return "", fmt.Errorf("尚未配置 AccessKey")
	}
	if !a.tryBusy() {
		return "", fmt.Errorf("已有任务在运行，请等待完成")
	}

	cfg := a.cfg
	certDir := a.certDir

	go func() {
		defer func() {
			a.mu.Lock()
			a.busy = false
			a.mu.Unlock()
		}()

	a.log("info", "提交申请：%s", strings.Join(clean, ", "))
	a.log("info", "ACME 目录：%s", cfg.ACMEDirURL)

	// 实时进度：状态落 a.applyProgress + 事件推送到前端
	setProgress := func(p aliyun.Progress) {
		a.mu.Lock()
		a.applyProgress = &p
		a.mu.Unlock()
		wruntime.EventsEmit(a.ctx, "apply:progress", p)
	}
	setProgress(aliyun.Progress{Stage: "init", Title: "申请证书", Detail: "提交申请：" + strings.Join(clean, ", "), Level: "info"})

	res, err := acme.Obtain(acme.Options{
		Email:    cfg.ACMEEmail,
		CADirURL: cfg.ACMEDirURL,
		EABKid:   cfg.EABKid,
		EABHmac:  cfg.EABHmac,
		Domains:  clean,
		AKID:     cfg.AccessKeyID,
		AKSecret: cfg.AccessKeySecret,
		KeyType:  cfg.KeyType,
		OnProgress: func(stage, detail, level string, pct int) {
			if level == "" {
				level = "info"
			}
			setProgress(aliyun.Progress{
				Stage: stage, Title: "申请证书", Detail: detail,
				Percent: pct, Level: level,
			})
			a.log(level, "%s", detail)
		},
	}, certDir)

	if err != nil {
		a.log("err", "申请失败：%v", err)
		setProgress(aliyun.Progress{Stage: "done", Title: "申请证书", Detail: "申请失败：" + err.Error(), Level: "err", Percent: 100})
		wruntime.EventsEmit(a.ctx, "apply:done", map[string]any{"ok": false, "error": err.Error()})
		return
	}

		a.log("ok", "签发成功，有效期至 %s（%d 天，签发者 %s）", res.NotAfter, res.Days, res.Issuer)
		a.log("info", "证书已保存：%s", res.CertPath)

		rec := store.IssuedCert{
			Key:       res.Primary,
			Domains:   res.Domains,
			CertPath:  res.CertPath,
			KeyPath:   res.KeyPath,
			NotAfter:  res.NotAfter,
			Days:      res.Days,
			Issuer:    res.Issuer,
			CA:        res.CA,
			CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		}
		a.mu.Lock()
		replaced := false
		for i := range a.issued {
			if a.issued[i].Key == rec.Key {
				a.issued[i] = rec
				replaced = true
				break
			}
		}
		if !replaced {
			a.issued = append(a.issued, rec)
		}
		a.mu.Unlock()
		a.saveIssued()

		wruntime.EventsEmit(a.ctx, "apply:done", map[string]any{"ok": true, "result": rec})
	}()

	return "started", nil
}

// ---------- 部署 ----------

// DeployRequest 部署请求
type DeployRequest struct {
	CertKey  string `json:"cert_key"`  // 本地证书标识（主域名）
	Targets  []string `json:"targets"` // oss / cdn / slb
	Domain   string `json:"domain"`    // 指定域名，空则自动匹配全部
	Region   string `json:"region"`    // SLB 用
	LBID     string `json:"lb_id"`     // SLB 实例 ID
	Port     int    `json:"port"`      // HTTPS 监听端口，默认 443
}

// ListSLBListeners 列出地域内 HTTPS 监听，供选择部署目标
func (a *App) ListSLBListeners(region string) ([]deploy.ListenerInfo, error) {
	if a.cfg.AccessKeyID == "" {
		return nil, fmt.Errorf("尚未配置 AccessKey")
	}
	cl := aliyun.NewClient(a.cfg.AccessKeyID, a.cfg.AccessKeySecret)
	return deploy.ListSLBHTTPSListeners(cl, region)
}

// certCovers 判断证书是否覆盖某主机（支持通配符）
func certCovers(domains []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == host {
			return true
		}
		if strings.HasPrefix(d, "*.") {
			base := strings.TrimPrefix(d, "*.")
			if host == base || strings.HasSuffix(host, "."+base) {
				return true
			}
		}
	}
	return false
}

// StartDeploy 部署证书（异步），完成后推送 deploy:done
func (a *App) StartDeploy(req DeployRequest) (string, error) {
	if a.cfg.AccessKeyID == "" {
		return "", fmt.Errorf("尚未配置 AccessKey")
	}
	if !a.tryBusy() {
		return "", fmt.Errorf("已有任务在运行，请等待完成")
	}

	a.mu.Lock()
	var rec *store.IssuedCert
	for i := range a.issued {
		if a.issued[i].Key == req.CertKey {
			r := a.issued[i]
			rec = &r
			break
		}
	}
	scan := a.result
	a.mu.Unlock()

	if rec == nil {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		return "", fmt.Errorf("未找到证书记录：%s", req.CertKey)
	}
	if scan == nil {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		return "", fmt.Errorf("尚无扫描结果，请先执行一次检测")
	}

	certPEM, err := os.ReadFile(rec.CertPath)
	if err != nil {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		return "", fmt.Errorf("读取证书失败: %w", err)
	}
	keyPEM, err := os.ReadFile(rec.KeyPath)
	if err != nil {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		return "", fmt.Errorf("读取私钥失败: %w", err)
	}

	cfg := a.cfg
	cert := deploy.Cert{
		Name:    rec.Key + "-" + time.Now().Format("20060102"),
		CertPEM: string(certPEM),
		KeyPEM:  string(keyPEM),
	}

	// 实时进度：状态落 a.deployProgress + 事件推送到前端
	setProgress := func(p aliyun.Progress) {
		a.mu.Lock()
		a.deployProgress = &p
		a.mu.Unlock()
		wruntime.EventsEmit(a.ctx, "deploy:progress", p)
	}
	setProgress(aliyun.Progress{Stage: "init", Title: "部署证书", Detail: "证书：" + rec.Key, Level: "info", Percent: 5})

	go func() {
		defer func() {
			a.mu.Lock()
			a.busy = false
			a.mu.Unlock()
		}()

		cl := aliyun.NewClient(cfg.AccessKeyID, cfg.AccessKeySecret)

		// 先统计部署任务清单（用于精确的进度百分比）
		type depTask struct {
			kind, name, bucket, region string
		}
		var tasks []depTask
		for _, e := range scan.Certs {
			if req.Domain != "" && e.Name != req.Domain {
				continue
			}
			if !certCovers(rec.Domains, e.Name) {
				continue
			}
			if contains(req.Targets, "oss") && e.Source == "oss" && e.Bucket != "" {
				tasks = append(tasks, depTask{"oss", e.Name, e.Bucket, e.Region})
			}
			if contains(req.Targets, "cdn") && e.Source == "cdn" {
				tasks = append(tasks, depTask{"cdn", e.Name, "", ""})
			}
		}

		// 需要引用证书管家时先上传一次
		needCAS := contains(req.Targets, "oss") || contains(req.Targets, "cdn")
		if needCAS && rec.CertID == "" {
			setProgress(aliyun.Progress{Stage: "cas", Title: "部署证书", Detail: "上传证书到证书管家…", Level: "info", Percent: 12})
			id, err := deploy.UploadToCAS(cl, cert)
			if err != nil {
				a.log("warn", "上传到证书管家失败，改用直传 PEM：%v", err)
			} else if id != "" {
				cert.CertID = id
				a.log("ok", "已上传到证书管家，CertId=%s", id)
				a.mu.Lock()
				for i := range a.issued {
					if a.issued[i].Key == rec.Key {
						a.issued[i].CertID = id
						break
					}
				}
				a.mu.Unlock()
				a.saveIssued()
			}
			setProgress(aliyun.Progress{Stage: "cas", Title: "部署证书", Detail: "证书管家就绪", Level: "info", Percent: 28})
		} else if rec.CertID != "" {
			cert.CertID = rec.CertID
		}

		var errs []string
		done := 0

		for i, t := range tasks {
			pct := 35
			if n := len(tasks); n > 0 {
				pct = 35 + (i+1)*50/n
			}
			if t.kind == "oss" {
				setProgress(aliyun.Progress{Stage: "oss", Title: "部署证书", Detail: "更新 OSS 域名 " + t.name + "（bucket " + t.bucket + "）…", Level: "info", Percent: pct})
				if err := deploy.DeployOSS(cl, t.bucket, t.region, t.name, cert); err != nil {
					errs = append(errs, fmt.Sprintf("OSS %s: %v", t.name, err))
					a.log("err", "OSS 部署失败 %s：%v", t.name, err)
				} else {
					done++
					a.log("ok", "OSS 已更新：%s（bucket %s）", t.name, t.bucket)
				}
			}
			if t.kind == "cdn" {
				setProgress(aliyun.Progress{Stage: "cdn", Title: "部署证书", Detail: "更新 CDN 域名 " + t.name + "…", Level: "info", Percent: pct})
				if err := deploy.DeployCDN(cl, t.name, cert); err != nil {
					errs = append(errs, fmt.Sprintf("CDN %s: %v", t.name, err))
					a.log("err", "CDN 部署失败 %s：%v", t.name, err)
				} else {
					done++
					a.log("ok", "CDN 已更新：%s", t.name)
				}
			}
		}

		if contains(req.Targets, "slb") {
			setProgress(aliyun.Progress{Stage: "slb", Title: "部署证书", Detail: "更新 SLB 监听证书…", Level: "info", Percent: 90})
			port := req.Port
			if port == 0 {
				port = 443
			}
			region := req.Region
			if region == "" && len(cfg.Regions) > 0 {
				region = cfg.Regions[0]
			}
			if err := deploy.DeploySLB(cl, region, req.LBID, port, cert); err != nil {
				errs = append(errs, fmt.Sprintf("SLB %s:%d: %v", req.LBID, port, err))
				a.log("err", "SLB 部署失败：%v", err)
			} else {
				done++
				a.log("ok", "SLB 已更新：%s:%d", req.LBID, port)
			}
		}

		if done == 0 && len(errs) == 0 {
			a.log("warn", "没有匹配到可部署的目标（域名是否已被该证书覆盖？）")
			setProgress(aliyun.Progress{Stage: "done", Title: "部署证书", Detail: "没有匹配到可部署的目标（域名是否已被该证书覆盖？）", Level: "err", Percent: 100})
		} else {
			a.log("ok", "部署完成：成功 %d 项，失败 %d 项", done, len(errs))
			lvl := "ok"
			detail := fmt.Sprintf("部署完成：成功 %d 项，失败 %d 项", done, len(errs))
			if len(errs) > 0 {
				lvl = "err"
				detail = fmt.Sprintf("部署完成：成功 %d 项，失败 %d 项（%s）", done, len(errs), strings.Join(errs, "；"))
			}
			setProgress(aliyun.Progress{Stage: "done", Title: "部署证书", Detail: detail, Level: lvl, Percent: 100})
		}

		wruntime.EventsEmit(a.ctx, "deploy:done", map[string]any{
			"ok": len(errs) == 0, "done": done, "errors": errs,
		})

		// 部署后自动刷新一次，让面板显示新的到期时间
		_, _ = a.StartScan()
	}()

	return "started", nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------- 无人值守：一键/自动续期 ----------

// AutoRenewItem 单个部署目标的执行结果
type AutoRenewItem struct {
	Target string `json:"target"` // 域名 / 监听
	Source string `json:"source"` // oss / cdn / slb
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// AutoRenewGroup 一次续期分组（一张证书覆盖的所有目标）
type AutoRenewGroup struct {
	Key      string          `json:"key"`      // 分组标识（本地已签发证书的 key，或域名）
	Domains  []string        `json:"domains"`  // 本次申请的域名集合
	Triggers []string        `json:"triggers"` // 触发续期的条目（源 + 域名 + 剩余天数）
	Applied  bool            `json:"applied"`  // 是否成功签发
	Items    []AutoRenewItem `json:"items"`    // 各部署目标结果
	Error    string          `json:"error,omitempty"`
}

// AutoRenewReport 一次自动续期的完整报告
type AutoRenewReport struct {
	Time     time.Time        `json:"time"`
	Manual   bool             `json:"manual"` // 是否手动触发
	Scanned  int              `json:"scanned"`
	Renewed  int              `json:"renewed"`  // 成功签发的证书数
	Deployed int              `json:"deployed"` // 成功部署的目标数
	Failed   int              `json:"failed"`   // 失败目标数
	Groups   []AutoRenewGroup `json:"groups"`
	MailSent bool             `json:"mail_sent"`
	MailErr  string           `json:"mail_error,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// RunAutoRenewNow 手动立即执行一次自动续期（同步返回报告，供前端按钮调用）
func (a *App) RunAutoRenewNow() AutoRenewReport {
	return a.runAutoRenew(true)
}

// GetAutoStatus 返回自动续期配置与上次执行结果（供设置页展示）
func (a *App) GetAutoStatus() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := map[string]any{
		"enabled":   a.cfg.AutoEnabled,
		"hour":      a.cfg.AutoHour,
		"threshold": a.cfg.AutoThreshold,
		"targets":   a.cfg.AutoTargets,
		"last_run":  a.lastAutoRun,
		"last_text": a.lastAutoText,
	}
	return st
}

// runAutoRenew 执行一轮自动续期：扫描 → 挑出临期证书 → 按证书分组续期 → 部署回原位置 → 邮件汇总
func (a *App) runAutoRenew(manual bool) AutoRenewReport {
	rep := AutoRenewReport{Time: time.Now(), Manual: manual}
	if a.cfg.AccessKeyID == "" {
		rep.Error = "尚未配置 AccessKey"
		a.log("err", "自动续期中止：%s", rep.Error)
		return rep
	}
	if !a.tryBusy() {
		rep.Error = "已有任务在运行，本轮跳过"
		a.log("warn", "自动续期跳过：%s", rep.Error)
		return rep
	}
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	threshold := a.cfg.AutoThreshold
	if threshold <= 0 {
		threshold = 15
	}
	targets := a.cfg.AutoTargets
	if len(targets) == 0 {
		targets = []string{"oss", "cdn", "slb"}
	}

	if manual {
		a.log("info", "手动触发一键续期（阈值 %d 天，目标 %s）", threshold, strings.Join(targets, "/"))
	} else {
		a.log("info", "自动续期任务启动（阈值 %d 天，目标 %s）", threshold, strings.Join(targets, "/"))
	}

	// 1) 扫描
	res := a.doScan()
	if res == nil {
		rep.Error = "扫描失败"
		a.log("err", "自动续期中止：%s", rep.Error)
		a.finishAutoRun(&rep)
		return rep
	}
	rep.Scanned = len(res.Certs)

	// 2) 挑出临期/过期的条目（cas 与 dns 来源不自动处理）
	var need []store.CertEntry
	for _, e := range res.Certs {
		if e.Days == nil || *e.Days >= threshold {
			continue
		}
		if e.Source == "cas" || e.Source == "dns" {
			continue
		}
		if !contains(targets, e.Source) {
			continue
		}
		need = append(need, e)
	}
	if len(need) == 0 {
		a.log("ok", "自动续期：没有剩余不足 %d 天的证书，无需处理", threshold)
		a.finishAutoRun(&rep)
		return rep
	}
	a.log("info", "发现 %d 条临期/过期记录，开始分组续期", len(need))

	// 3) 分组：优先复用本地已签发证书的域名组合（保持原 SAN 结构），否则按条目自身域名
	a.mu.Lock()
	issued := make([]store.IssuedCert, len(a.issued))
	copy(issued, a.issued)
	a.mu.Unlock()

	groups := map[string]*AutoRenewGroup{}
	slbGroupByCertID := map[string]string{}  // SLB 服务器证书 ID → 分组 key
	slbGroupByName := map[string]string{}    // SLB 服务器证书名 → 分组 key
	var order []string

	for _, e := range need {
		key := ""
		var domains []string
		for _, ic := range issued {
			if certCovers(ic.Domains, e.Name) {
				key = ic.Key
				domains = append([]string{}, ic.Domains...)
				break
			}
		}
		if key == "" {
			// 非面板签发的证书：用条目自身覆盖的域名（无 SAN 信息时退化为域名本身）
			domains = append(domains, e.Name)
			for _, d := range e.Domains {
				if d != e.Name {
					domains = append(domains, d)
				}
			}
			key = e.Source + ":" + e.Name
		}
		g, ok := groups[key]
		if !ok {
			g = &AutoRenewGroup{Key: key, Domains: domains}
			groups[key] = g
			order = append(order, key)
		}
		g.Triggers = append(g.Triggers, fmt.Sprintf("%s %s（剩余 %d 天）", strings.ToUpper(e.Source), e.Name, *e.Days))
		if e.Source == "slb" {
			if e.ID != "" {
				slbGroupByCertID[e.ID] = key
			}
			if e.Name != "" {
				slbGroupByName[e.Name] = key
			}
		}
	}

	cl := aliyun.NewClient(a.cfg.AccessKeyID, a.cfg.AccessKeySecret)

	// 4) 逐组：申请 → 上传 CAS → 部署到所有覆盖的目标
	for _, key := range order {
		g := groups[key]
		a.log("info", "续期组 %s：域名 %s", key, strings.Join(g.Domains, ", "))

		out, err := acme.Obtain(acme.Options{
			Email:    a.cfg.ACMEEmail,
			CADirURL: a.cfg.ACMEDirURL,
			EABKid:   a.cfg.EABKid,
			EABHmac:  a.cfg.EABHmac,
			Domains:  g.Domains,
			AKID:     a.cfg.AccessKeyID,
			AKSecret: a.cfg.AccessKeySecret,
			KeyType:  a.cfg.KeyType,
		}, a.certDir)
		if err != nil {
			g.Error = "签发失败：" + err.Error()
			rep.Failed++
			a.log("err", "续期组 %s 签发失败：%v", key, err)
			continue
		}
		g.Applied = true
		rep.Renewed++
		a.log("ok", "续期组 %s 签发成功，有效期至 %s", key, out.NotAfter)

		certPEM, err := os.ReadFile(out.CertPath)
		if err != nil {
			g.Error = "读取证书失败：" + err.Error()
			rep.Failed++
			a.log("err", "续期组 %s 读取证书失败：%v", key, err)
			continue
		}
		keyPEM, err := os.ReadFile(out.KeyPath)
		if err != nil {
			g.Error = "读取私钥失败：" + err.Error()
			rep.Failed++
			a.log("err", "续期组 %s 读取私钥失败：%v", key, err)
			continue
		}
		cert := deploy.Cert{
			Name:    key + "-" + time.Now().Format("20060102"),
			CertPEM: string(certPEM),
			KeyPEM:  string(keyPEM),
		}

		// OSS/CDN 需要引用证书管家证书
		if contains(targets, "oss") || contains(targets, "cdn") {
			if id, err := deploy.UploadToCAS(cl, cert); err != nil {
				a.log("warn", "续期组 %s 上传证书管家失败，改用直传 PEM：%v", key, err)
			} else if id != "" {
				cert.CertID = id
			}
		}

		// 4a) OSS / CDN：按证书覆盖关系匹配所有目标
		for _, e := range res.Certs {
			if e.Source != "oss" && e.Source != "cdn" {
				continue
			}
			if !contains(targets, e.Source) {
				continue
			}
			if !certCovers(g.Domains, e.Name) {
				continue
			}
			var derr error
			if e.Source == "oss" && e.Bucket != "" {
				derr = deploy.DeployOSS(cl, e.Bucket, e.Region, e.Name, cert)
			} else if e.Source == "cdn" {
				derr = deploy.DeployCDN(cl, e.Name, cert)
			} else {
				continue
			}
			if derr != nil {
				g.Items = append(g.Items, AutoRenewItem{Target: e.Name, Source: e.Source, Error: derr.Error()})
				rep.Failed++
				a.log("err", "%s 部署失败 %s：%v", strings.ToUpper(e.Source), e.Name, derr)
			} else {
				g.Items = append(g.Items, AutoRenewItem{Target: e.Name, Source: e.Source, OK: true})
				rep.Deployed++
				a.log("ok", "%s 已更新：%s", strings.ToUpper(e.Source), e.Name)
			}
		}

		// 4b) SLB：按监听当前使用的证书匹配替换（只动临期证书所在的监听）
		if contains(targets, "slb") {
			for _, region := range a.cfg.Regions {
				ls, err := deploy.ListSLBHTTPSListeners(cl, region)
				if err != nil {
					a.log("warn", "查询 %s 的 SLB 监听失败：%v", region, err)
					continue
				}
				for _, l := range ls {
					gk, byID := slbGroupByCertID[l.CertID]
					if !byID {
						gk, _ = slbGroupByName[l.CertName]
					}
					if gk != key {
						continue
					}
					tgt := fmt.Sprintf("%s:%d", l.LoadBalancer, l.Port)
					if err := deploy.DeploySLB(cl, l.Region, l.LoadBalancerID, l.Port, cert); err != nil {
						g.Items = append(g.Items, AutoRenewItem{Target: tgt, Source: "slb", Error: err.Error()})
						rep.Failed++
						a.log("err", "SLB 部署失败 %s：%v", tgt, err)
					} else {
						g.Items = append(g.Items, AutoRenewItem{Target: tgt, Source: "slb", OK: true})
						rep.Deployed++
						a.log("ok", "SLB 已更新：%s", tgt)
					}
				}
			}
		}

		rep.Groups = append(rep.Groups, *g)
	}

	// 5) 收尾：写日志、发邮件
	a.finishAutoRun(&rep)
	return rep
}

// finishAutoRun 记录执行结果并发送汇总邮件
func (a *App) finishAutoRun(rep *AutoRenewReport) {
	if rep.Error == "" {
		a.log("ok", "自动续期完成：签发 %d 张，部署成功 %d 项，失败 %d 项",
			rep.Renewed, rep.Deployed, rep.Failed)
	}

	// 邮件汇总（未配置 SMTP 时静默跳过）
	if a.cfg.SMTPHost != "" && a.cfg.MailTo != "" {
		subject, body := a.autoMailBody(rep)
		if err := notify.Send(notify.Config{
			Host: a.cfg.SMTPHost, Port: a.cfg.SMTPPort,
			User: a.cfg.SMTPUser, Pass: a.cfg.SMTPPass,
			To: a.cfg.MailTo, From: a.cfg.MailFrom,
		}, subject, body); err != nil {
			rep.MailErr = err.Error()
			a.log("warn", "续期汇总邮件发送失败：%v", err)
		} else {
			rep.MailSent = true
			a.log("ok", "续期汇总邮件已发送至 %s", a.cfg.MailTo)
		}
	} else {
		a.log("info", "未配置 SMTP，跳过邮件汇总（可在设置页填写）")
	}

	text := fmt.Sprintf("签发 %d 张 / 部署 %d 项 / 失败 %d 项", rep.Renewed, rep.Deployed, rep.Failed)
	a.mu.Lock()
	a.lastAutoRun = rep.Time.Format("2006-01-02 15:04")
	a.lastAutoText = text
	a.mu.Unlock()
	wruntime.EventsEmit(a.ctx, "auto:done", rep)
}

// autoMailBody 生成邮件主题与正文
func (a *App) autoMailBody(rep *AutoRenewReport) (string, string) {
	tag := "自动"
	if rep.Manual {
		tag = "手动"
	}
	subject := fmt.Sprintf("SSL %s续期报告 %s：签发 %d 张，部署 %d 项，失败 %d 项",
		tag, rep.Time.Format("2006-01-02"), rep.Renewed, rep.Deployed, rep.Failed)

	var b strings.Builder
	fmt.Fprintf(&b, "执行时间：%s（%s触发）\n", rep.Time.Format("2006-01-02 15:04:05"), tag)
	fmt.Fprintf(&b, "扫描条目：%d 条\n", rep.Scanned)
	if rep.Error != "" {
		fmt.Fprintf(&b, "执行异常：%s\n", rep.Error)
	}
	fmt.Fprintf(&b, "结果汇总：签发 %d 张，部署成功 %d 项，失败 %d 项\n", rep.Renewed, rep.Deployed, rep.Failed)

	for _, g := range rep.Groups {
		b.WriteString("\n—— " + g.Key + " ——\n")
		fmt.Fprintf(&b, "申请域名：%s\n", strings.Join(g.Domains, ", "))
		if g.Error != "" {
			fmt.Fprintf(&b, "结果：失败（%s）\n", g.Error)
		} else {
			fmt.Fprintf(&b, "结果：签发成功，部署 %d 项\n", len(g.Items))
		}
		if len(g.Triggers) > 0 {
			b.WriteString("触发原因：" + strings.Join(g.Triggers, "；") + "\n")
		}
		for _, it := range g.Items {
			if it.OK {
				fmt.Fprintf(&b, "  ✅ [%s] %s\n", strings.ToUpper(it.Source), it.Target)
			} else {
				fmt.Fprintf(&b, "  ❌ [%s] %s：%s\n", strings.ToUpper(it.Source), it.Target, it.Error)
			}
		}
	}
	if len(rep.Groups) == 0 && rep.Error == "" {
		b.WriteString("\n本次没有需要续期的证书（均在阈值天数以上）。\n")
	}
	b.WriteString("\n—— 本邮件由 SSL 证书面板自动发送 ——\n")
	return subject, b.String()
}

// startAutoLoop 面板内定时器：每天到点执行一次自动续期（需保持面板运行）
func (a *App) startAutoLoop() {
	lastDay := ""
	go func() {
		for {
			time.Sleep(2 * time.Minute)
			a.mu.Lock()
			en := a.cfg.AutoEnabled
			hour := a.cfg.AutoHour
			a.mu.Unlock()
			if !en {
				continue
			}
			now := time.Now()
			day := now.Format("2006-01-02")
			// 到点后在该小时窗口内触发一次（例如 9 点 → 09:00~09:59 之间）
			if now.Hour() != hour || lastDay == day {
				continue
			}
			lastDay = day
			a.runAutoRenew(false)
		}
	}()
}

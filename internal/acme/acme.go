package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// 常用 ACME 目录地址
const (
	ZeroSSLDirURL = "https://acme.zerossl.com/v2/DV90"
	LEStagingURL  = "https://acme-staging-v02.api.letsencrypt.org/directory"
	LEProdURL     = "https://acme-v02.api.letsencrypt.org/directory"
)

// acmeUser 实现 lego 的 registration.User 接口
type acmeUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Options 申请参数
type Options struct {
	Email    string   // ACME 账号邮箱（必填）
	CADirURL string   // ACME 目录地址，默认 ZeroSSL
	EABKid   string   // ZeroSSL EAB KID（ZeroSSL 必填）
	EABHmac  string   // ZeroSSL EAB HMAC Key
	Domains  []string // 主域名放第一个；支持 *.example.com 通配符
	AKID     string   // 阿里云 AccessKey ID（DNS-01 用）
	AKSecret string
	KeyType  string // rsa2048(默认) / rsa4096 / ec256 / ec384

	// OnProgress 实时进度回调：stage 阶段标识 / detail 动作描述 / level info|ok|warn|err / percent 0-100
	OnProgress func(stage, detail, level string, percent int)
}

// Result 申请结果
type Result struct {
	Primary    string   `json:"primary"`
	Domains    []string `json:"domains"`
	CertPath   string   `json:"cert_path"`
	KeyPath    string   `json:"key_path"`
	FullChain  string   `json:"fullchain_pem"`
	PrivateKey string   `json:"key_pem"`
	NotAfter   string   `json:"not_after"`
	Days       int      `json:"days"`
	Issuer     string   `json:"issuer"`
	CA         string   `json:"ca"`
}

// Obtain 走 DNS-01 申请证书，落盘到 outDir，并返回 PEM 内容
func Obtain(opts Options, outDir string) (*Result, error) {
	opts.Email = strings.TrimSpace(opts.Email)
	if opts.Email == "" {
		return nil, fmt.Errorf("缺少 ACME 账号邮箱")
	}
	if strings.TrimSpace(opts.AKID) == "" || strings.TrimSpace(opts.AKSecret) == "" {
		return nil, fmt.Errorf("缺少 AccessKey")
	}
	if len(opts.Domains) == 0 {
		return nil, fmt.Errorf("缺少域名")
	}
	if strings.TrimSpace(opts.CADirURL) == "" {
		opts.CADirURL = ZeroSSLDirURL
	}

	emit := func(stage, detail, level string, pct int) {
		if opts.OnProgress != nil {
			opts.OnProgress(stage, detail, level, pct)
		}
	}
	emit("init", "准备 ACME 客户端…", "info", 5)

	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建证书目录失败: %w", err)
	}

	key, err := loadOrCreateAccountKey(filepath.Join(outDir, "account.key"))
	if err != nil {
		return nil, err
	}
	user := &acmeUser{Email: opts.Email, key: key}

	cfg := lego.NewConfig(user)
	cfg.CADirURL = opts.CADirURL
	cfg.Certificate.KeyType = parseKeyType(opts.KeyType)
	cfg.UserAgent = "sslpanel-app/1.0"

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("初始化 ACME 客户端失败: %w", err)
	}

	provider, err := NewAliDNSProvider(opts.AKID, opts.AKSecret)
	if err != nil {
		return nil, err
	}
	// 挂接 DNS-01 关键节点进度：写入 TXT / 验证后清理
	if p, ok := provider.(*alidnsProvider); ok {
		p.onPresent = func(fqdn string) {
			emit("dns", "TXT 已写入 "+fqdn+"，等待 DNS 生效（最长 2 分钟）…", "info", 50)
		}
		p.onPresenting = func(fqdn string) {
			emit("dns", "正在写入验证记录 "+fqdn+" …", "info", 40)
		}
		p.onCleanup = func(fqdn string) {
			emit("verify", fqdn+" 验证完成，清理 TXT 记录…", "info", 85)
		}
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, fmt.Errorf("设置 DNS-01 挑战失败: %w", err)
	}

	// 复用已有账号，否则按是否提供 EAB 分别注册
	if reg, err := client.Registration.ResolveAccountByKey(); err == nil && reg != nil {
		user.Registration = reg
	} else if strings.TrimSpace(opts.EABKid) != "" {
		reg, err := client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true,
			Kid:                  strings.TrimSpace(opts.EABKid),
			HmacEncoded:          strings.TrimSpace(opts.EABHmac),
		})
		if err != nil {
			return nil, fmt.Errorf("ACME 账号注册失败（请检查 EAB KID / HMAC 是否正确）: %w", err)
		}
		user.Registration = reg
	} else {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("ACME 账号注册失败: %w", err)
		}
		user.Registration = reg
	}
	emit("account", "ACME 账号就绪，开始构造验证请求…", "info", 25)

	domains := normalizeDomains(opts.Domains)
	emit("challenge", "向 CA 提交域名验证（"+strings.Join(domains, ", ")+"）…", "info", 35)

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("签发失败（%s）: %w", strings.Join(domains, ", "), err)
	}

	primary := strings.TrimPrefix(domains[0], "*.")
	safe := strings.ReplaceAll(primary, "*", "_")
	certPath := filepath.Join(outDir, safe+".crt")
	keyPath := filepath.Join(outDir, safe+".key")

	if err := os.WriteFile(certPath, res.Certificate, 0o600); err != nil {
		return nil, fmt.Errorf("写入证书失败: %w", err)
	}
	if err := os.WriteFile(keyPath, res.PrivateKey, 0o600); err != nil {
		return nil, fmt.Errorf("写入私钥失败: %w", err)
	}

	out := &Result{
		Primary:    primary,
		Domains:    domains,
		CertPath:   certPath,
		KeyPath:    keyPath,
		FullChain:  string(res.Certificate),
		PrivateKey: string(res.PrivateKey),
		CA:         opts.CADirURL,
	}

	if blk, _ := pem.Decode(res.Certificate); blk != nil {
		if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
			out.NotAfter = c.NotAfter.UTC().Format("2006-01-02")
			out.Days = int(time.Until(c.NotAfter).Hours() / 24)
			out.Issuer = c.Issuer.CommonName
		}
	}
	emit("done", fmt.Sprintf("签发成功：%s，有效期至 %s", out.Primary, out.NotAfter), "ok", 100)
	return out, nil
}

// normalizeDomains 去重、去空、小写；通配符排在前面
func normalizeDomains(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.HasPrefix(out[i], "*.") && !strings.HasPrefix(out[j], "*.")
	})
	return out
}

func parseKeyType(s string) certcrypto.KeyType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rsa4096":
		return certcrypto.RSA4096
	case "ec256":
		return certcrypto.EC256
	case "ec384":
		return certcrypto.EC384
	default:
		return certcrypto.RSA2048
	}
}

// loadOrCreateAccountKey 复用或生成 ACME 账号私钥（PKCS8 PEM 落盘）
func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成账号密钥失败: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("序列化账号密钥失败: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("保存账号密钥失败: %w", err)
	}
	return key, nil
}

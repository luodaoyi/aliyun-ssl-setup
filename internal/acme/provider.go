// Package acme 实现 ACME 证书申请：lego 客户端 + 阿里云云解析 DNS-01 挑战。
//
// 说明：lego 官方 alidns provider 依赖 github.com/aliyun/alibaba-cloud-sdk-go（体积庞大的旧版 SDK），
// 这里基于项目内已有的轻量 RPC 客户端自行实现 challenge.Provider，功能一致但依赖为零。
package acme

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sslpanel-app/internal/aliyun"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

const (
	dnsEndpoint = "alidns.aliyuncs.com"
	dnsVersion  = "2015-01-09"
)

// alidnsProvider 通过阿里云云解析完成 DNS-01 挑战
type alidnsProvider struct {
	cl  *aliyun.Client
	ttl string
}

// NewAliDNSProvider 构造 DNS-01 挑战提供者
func NewAliDNSProvider(akid, secret string) (challenge.Provider, error) {
	if strings.TrimSpace(akid) == "" || strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("缺少 AccessKey，无法进行 DNS-01 验证")
	}
	return &alidnsProvider{cl: aliyun.NewClient(akid, secret), ttl: "600"}, nil
}

// Timeout 告诉 lego 等待 DNS 传播的超时与轮询间隔（阿里云解析生效较快，留足余量）
func (p *alidnsProvider) Timeout() (time.Duration, time.Duration) {
	return 120 * time.Second, 5 * time.Second
}

// Present 添加 _acme-challenge TXT 记录
func (p *alidnsProvider) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)

	zone, err := p.findZone(info.EffectiveFQDN)
	if err != nil {
		return fmt.Errorf("alicloud: %w", err)
	}
	rr, err := dns01.ExtractSubDomain(info.EffectiveFQDN, zone)
	if err != nil {
		return fmt.Errorf("alicloud: %w", err)
	}

	_, err = p.cl.RPCCall(dnsEndpoint, dnsVersion, "AddDomainRecord", map[string]string{
		"DomainName": zone,
		"RR":        rr,
		"Type":      "TXT",
		"Value":     info.Value,
		"TTL":       p.ttl,
	})
	if err != nil {
		return fmt.Errorf("alicloud: 添加 TXT 记录失败（%s.%s）: %w", rr, zone, err)
	}
	return nil
}

// CleanUp 清除验证用 TXT 记录
func (p *alidnsProvider) CleanUp(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)

	zone, err := p.findZone(info.EffectiveFQDN)
	if err != nil {
		return fmt.Errorf("alicloud: %w", err)
	}
	rr, err := dns01.ExtractSubDomain(info.EffectiveFQDN, zone)
	if err != nil {
		return fmt.Errorf("alicloud: %w", err)
	}

	records, err := p.listRecords(zone)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Type == "TXT" && r.RR == rr {
			_, _ = p.cl.RPCCall(dnsEndpoint, dnsVersion, "DeleteDomainRecord",
				map[string]string{"RecordId": r.RecordID})
		}
	}
	return nil
}

type dnsRecord struct {
	RecordID string `json:"RecordId"`
	RR       string `json:"RR"`
	Type     string `json:"Type"`
	Value    string `json:"Value"`
}

func (p *alidnsProvider) listRecords(zone string) ([]dnsRecord, error) {
	var out []dnsRecord
	for page := 1; page <= 20; page++ {
		raw, err := p.cl.RPCCall(dnsEndpoint, dnsVersion, "DescribeDomainRecords", map[string]string{
			"DomainName": zone,
			"PageNumber": fmt.Sprint(page),
			"PageSize":   "500",
		})
		if err != nil {
			return nil, fmt.Errorf("alicloud: 查询解析记录失败: %w", err)
		}
		var r struct {
			TotalCount    int `json:"TotalCount"`
			DomainRecords struct {
				Record []dnsRecord `json:"Record"`
			} `json:"DomainRecords"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("alicloud: 解析记录响应异常: %w", err)
		}
		out = append(out, r.DomainRecords.Record...)
		if len(r.DomainRecords.Record) == 0 || len(out) >= r.TotalCount {
			break
		}
	}
	return out, nil
}

// findZone 在账号权威域名列表中找最长后缀匹配（支持二级/三级域名）
func (p *alidnsProvider) findZone(fqdn string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(fqdn), ".")
	domains, err := p.listDomains()
	if err != nil {
		return "", err
	}
	best := ""
	for _, d := range domains {
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			if len(d) > len(best) {
				best = d
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("云解析中未找到 %s 所属的权威域名（该域名是否在本账号下？）", host)
	}
	return best, nil
}

func (p *alidnsProvider) listDomains() ([]string, error) {
	var out []string
	for page := 1; page <= 20; page++ {
		raw, err := p.cl.RPCCall(dnsEndpoint, dnsVersion, "DescribeDomains", map[string]string{
			"PageNumber": fmt.Sprint(page),
			"PageSize":   "100",
		})
		if err != nil {
			return nil, fmt.Errorf("查询域名列表失败: %w", err)
		}
		var r struct {
			TotalCount int `json:"TotalCount"`
			Domains    struct {
				Domain []struct {
					DomainName string `json:"DomainName"`
				} `json:"Domain"`
			} `json:"Domains"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("域名列表响应异常: %w", err)
		}
		for _, d := range r.Domains.Domain {
			if d.DomainName != "" {
				out = append(out, d.DomainName)
			}
		}
		if len(r.Domains.Domain) == 0 || len(out) >= r.TotalCount {
			break
		}
	}
	return out, nil
}

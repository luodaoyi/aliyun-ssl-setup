package aliyun

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"sslpanel-app/internal/store"
)

// Progress 扫描进度事件
type Progress struct {
	Stage   string `json:"stage"`   // 阶段标识：init / cas / slb / oss / cdn / dns / done
	Title   string `json:"title"`   // 阶段名（中文）
	Detail  string `json:"detail"`  // 当前动作描述
	Done    int    `json:"done"`    // 已完成项
	Total   int    `json:"total"`   // 总项数（0 表示总项未知）
	Percent int    `json:"percent"` // 0-100
	Level   string `json:"level"`   // info / ok / warn / err
}

// ProgressFn 进度回调
type ProgressFn func(Progress)

// Scanner 阿里云证书相关资源扫描器
type Scanner struct {
	cl         *Client
	Regions    []string
	OnProgress ProgressFn
}

func NewScanner(cl *Client, regions []string) *Scanner {
	return &Scanner{cl: cl, Regions: regions}
}

// emit 上报一次进度
func (s *Scanner) emit(stage, title, detail, level string, done, total int) {
	if s.OnProgress == nil {
		return
	}
	pct := 0
	if total > 0 {
		pct = done * 100 / total
		if pct > 100 {
			pct = 100
		}
	}
	s.OnProgress(Progress{
		Stage:   stage,
		Title:   title,
		Detail:  detail,
		Done:    done,
		Total:   total,
		Percent: pct,
		Level:   level,
	})
}

var stageNames = map[string]string{
	"init": "准备",
	"cas":  "CAS 证书管家",
	"slb":  "SLB 负载均衡",
	"oss":  "OSS 自定义域名",
	"cdn":  "CDN 加速域名",
	"dns":  "云解析域名",
	"done": "汇总",
}

func stageTitle(stage string) string {
	if t, ok := stageNames[stage]; ok {
		return t
	}
	return stage
}

// ScanAll 扫描 CAS / SLB / OSS / CDN / DNS，返回证书库存与错误
func (s *Scanner) ScanAll() ([]store.CertEntry, []string) {
	var (
		mu      sync.Mutex
		entries []store.CertEntry
		errs    []string
	)
	const totalStages = 5
	var finished int32

	s.emit("init", stageTitle("init"), "开始扫描 5 类云资源…", "info", 0, totalStages)

	add := func(es []store.CertEntry, e error, tag string) {
		mu.Lock()
		if e != nil {
			errs = append(errs, tag+": "+e.Error())
		}
		entries = append(entries, es...)
		mu.Unlock()

		n := int(atomic.AddInt32(&finished, 1))
		if e != nil {
			s.emit(tag, stageTitle(tag),
				fmt.Sprintf("完成 %d 条，但有错误：%s", len(es), e.Error()), "warn", n, totalStages)
		} else {
			s.emit(tag, stageTitle(tag),
				fmt.Sprintf("完成，%d 条", len(es)), "ok", n, totalStages)
		}
	}

	var wg sync.WaitGroup
	wg.Add(5)

	go func() { defer wg.Done(); es, err := s.scanCAS(); add(es, err, "cas") }()
	go func() { defer wg.Done(); es, err := s.scanSLB(); add(es, err, "slb") }()
	go func() { defer wg.Done(); es, err := s.scanOSS(); add(es, err, "oss") }()
	go func() { defer wg.Done(); es, err := s.scanCDN(); add(es, err, "cdn") }()
	go func() { defer wg.Done(); es, err := s.scanDNS(); add(es, err, "dns") }()

	wg.Wait()
	s.emit("done", stageTitle("done"), "扫描完成", "ok", totalStages, totalStages)
	return entries, errs
}

// scanCAS 证书管家证书列表（ListCertificates 2020-04-07，NotAfter 为 epoch 毫秒）
func (s *Scanner) scanCAS() ([]store.CertEntry, error) {
	s.emit("cas", stageTitle("cas"), "正在查询证书列表…", "info", 0, 1)

	raw, err := s.cl.RPCCall("cas.aliyuncs.com", "2020-04-07", "ListCertificates",
		map[string]string{"ShowSize": "100", "CurrentPage": "1"})
	if err != nil {
		// 兜底：试老版本
		var err2 error
		raw, err2 = s.cl.RPCCall("cas.aliyuncs.com", "2018-07-13", "ListCertificates", nil)
		if err2 != nil {
			return nil, err
		}
	}
	var r struct {
		CertificateList []struct {
			CertificateName string      `json:"CertificateName"`
			CertificateID   json.Number `json:"CertificateId"`
			CommonName      string      `json:"CommonName"`
			Domain          string      `json:"Domain"`
			Sans            string      `json:"Sans"`
			CertType        string      `json:"CertType"`
			Algorithm       string      `json:"Algorithm"`
			Issuer          string      `json:"Issuer"`
			NotBefore       json.Number `json:"NotBefore"`
			NotAfter        json.Number `json:"NotAfter"`
		} `json:"CertificateList"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}

	s.emit("cas", stageTitle("cas"),
		fmt.Sprintf("返回 %d 张证书，解析中…", len(r.CertificateList)), "info", 0, len(r.CertificateList))

	var out []store.CertEntry
	for i, c := range r.CertificateList {
		e := store.CertEntry{
			Source:  "cas",
			Name:    c.CertificateName,
			Domains: append(SansToList(c.Sans), c.CommonName, c.Domain),
			Issuer:  c.Issuer,
			ID:      c.CertificateID.String(),
			Region:  "cn-hangzhou",
		}
		if e.Name == "" {
			e.Name = "cert-" + c.CertificateID.String()
		}
		if nb, ok := ParseEpoch(c.NotBefore); ok {
			e.NotBefore = FmtDate(nb)
		}
		if na, ok := ParseEpoch(c.NotAfter); ok {
			e.NotAfter = FmtDate(na)
			e.Days = DaysLeft(na)
		}
		if c.CertType != "" {
			e.Note = c.CertType
		} else if c.Algorithm != "" {
			e.Note = c.Algorithm
		}
		out = append(out, e)

		if (i+1)%5 == 0 || i+1 == len(r.CertificateList) {
			s.emit("cas", stageTitle("cas"),
				fmt.Sprintf("解析证书 %d/%d：%s", i+1, len(r.CertificateList), e.Name),
				"info", i+1, len(r.CertificateList))
		}
	}
	return out, nil
}

// scanSLB 各地域负载均衡服务器证书（DescribeServerCertificates）
func (s *Scanner) scanSLB() ([]store.CertEntry, error) {
	var (
		out    []store.CertEntry
		errAll []string
	)
	total := len(s.Regions)
	s.emit("slb", stageTitle("slb"),
		fmt.Sprintf("正在查询 %d 个地域…", total), "info", 0, total)

	for i, region := range s.Regions {
		s.emit("slb", stageTitle("slb"),
			fmt.Sprintf("查询 %s（%d/%d）…", region, i+1, total), "info", i, total)

		raw, err := s.cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15",
			"DescribeServerCertificates", map[string]string{"RegionId": region})
		if err != nil {
			errAll = append(errAll, region+": "+err.Error())
			continue
		}
		var r struct {
			ServerCertificates struct {
				ServerCertificate []struct {
					ServerCertificateID   string      `json:"ServerCertificateId"`
					ServerCertificateName string      `json:"ServerCertificateName"`
					CommonName            string      `json:"CommonName"`
					Fingerprint           string      `json:"Fingerprint"`
					IsAliCloudCertificate any         `json:"IsAliCloudCertificate"` // 可能是 "true"/"false" 或 0/1
					ExpireTime            string      `json:"ExpireTime"`
					ExpireTimeStamp       json.Number `json:"ExpireTimeStamp"`
				} `json:"ServerCertificate"`
			} `json:"ServerCertificates"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			errAll = append(errAll, region+": 解析失败 "+err.Error())
			continue
		}
		before := len(out)
		for _, c := range r.ServerCertificates.ServerCertificate {
			e := store.CertEntry{
				Source:  "slb",
				Name:    c.ServerCertificateName,
				ID:      c.ServerCertificateID,
				Region:  region,
				Domains: SansToList(c.CommonName),
			}
			if e.Name == "" {
				e.Name = "slbcert-" + c.ServerCertificateID
			}
			if fmt.Sprint(c.IsAliCloudCertificate) == "true" || fmt.Sprint(c.IsAliCloudCertificate) == "1" {
				e.Note = "阿里云托管证书"
			}
			na, ok := ParseEpoch(c.ExpireTimeStamp)
			if !ok {
				na, ok = ParseDateStr(c.ExpireTime)
			}
			if ok {
				e.NotAfter = FmtDate(na)
				e.Days = DaysLeft(na)
			}
			out = append(out, e)
		}
		s.emit("slb", stageTitle("slb"),
			fmt.Sprintf("%s 完成，%d 张证书", region, len(out)-before), "ok", i+1, total)
	}

	if len(errAll) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("%s", strings.Join(errAll, "; "))
	}
	if len(errAll) > 0 {
		out = append(out, store.CertEntry{Source: "slb", Name: "(部分地域查询失败)", Note: strings.Join(errAll, "; ")})
	}
	return out, nil
}

// scanOSS Bucket 自定义域名 + 托管证书；未托管证书的域名做 TLS 实测
func (s *Scanner) scanOSS() ([]store.CertEntry, error) {
	// OSS 分两个阶段（查 CNAME → TLS 实测），但对外只报一条连续的百分比：
	// 阶段一占 0~60%，阶段二占 60~100%，否则阶段二开始时进度会从 100% 掉回 0%，
	// 前端的总体进度（各阶段平均）会跟着倒退。
	const (
		phase1Weight = 60
		ossScale     = 100
	)
	s.emit("oss", stageTitle("oss"), "正在列出 Bucket…", "info", 0, ossScale)

	buckets, err := s.cl.OSSListBuckets()
	if err != nil {
		return nil, err
	}
	total := len(buckets)
	s.emit("oss", stageTitle("oss"),
		fmt.Sprintf("发现 %d 个 Bucket，查询绑定域名…", total), "info", 0, ossScale)

	// 阶段一：并发查询每个 Bucket 的 CNAME 绑定
	var (
		mu      sync.Mutex
		out     []store.CertEntry
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
		scanned int32
	)
	for _, b := range buckets {
		wg.Add(1)
		go func(name, loc string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var got []store.CertEntry
			res, err := s.cl.OSSGetCname(name, loc)
			if err == nil {
				for _, cn := range res.Cnames {
					e := store.CertEntry{
						Source: "oss",
						Name:   cn.Domain,
						Region: loc,
						Bucket: name,
					}
					if cn.Certificate != nil {
						e.ID = cn.Certificate.CertID
						e.Issuer = "OSS 托管证书(" + cn.Certificate.Type + ")"
						if na, ok := ParseDateStr(cn.Certificate.ValidEndDate); ok {
							e.NotAfter = FmtDate(na)
							e.Days = DaysLeft(na)
						}
					}
					got = append(got, e)
				}
			}
			mu.Lock()
			out = append(out, got...)
			mu.Unlock()

			n := int(atomic.AddInt32(&scanned, 1))
			if n%5 == 0 || n == total {
				s.emit("oss", stageTitle("oss"),
					fmt.Sprintf("查询绑定域名 %d/%d：%s", n, total, name), "info", n, total)
			}
		}(b.Name, b.Location)
	}
	wg.Wait()

	// 阶段二：未托管证书的域名做 TLS 实测（耗时最长，逐项上报）
	var pending []int
	for i := range out {
		if out[i].Issuer == "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		s.emit("oss", stageTitle("oss"),
			fmt.Sprintf("完成，%d 个域名（均有托管证书）", len(out)), "ok", total, total)
		return out, nil
	}

	s.emit("oss", stageTitle("oss"),
		fmt.Sprintf("发现 %d 个域名，开始 TLS 实测证书…", len(out)), "info", 0, len(out))

	var probed int32
	ptotal := len(pending)
	for _, idx := range pending {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			domain := out[i].Name
			if na, issuer, ok := TLSCertProbe(domain, 10_000_000_000); ok {
				out[i].NotAfter = FmtDate(na)
				out[i].Days = DaysLeft(na)
				out[i].Issuer = issuer
				out[i].Note = "未托管，TLS 实测"
			} else {
				out[i].Note = "未托管，未开 HTTPS"
			}

			n := int(atomic.AddInt32(&probed, 1))
			if n%5 == 0 || n == ptotal {
				s.emit("oss", stageTitle("oss"),
					fmt.Sprintf("TLS 实测 %d/%d：%s", n, ptotal, domain), "info", n, ptotal)
			}
		}(idx)
	}
	wg.Wait()

	s.emit("oss", stageTitle("oss"),
		fmt.Sprintf("完成，%d 个域名", len(out)), "ok", total, total)
	return out, nil
}

// scanCDN 加速域名列表 + TLS 实测证书
func (s *Scanner) scanCDN() ([]store.CertEntry, error) {
	s.emit("cdn", stageTitle("cdn"), "正在查询加速域名列表…", "info", 0, 0)

	raw, err := s.cl.RPCCall("cdn.aliyuncs.com", "2018-05-10", "DescribeUserDomains",
		map[string]string{"PageSize": "100", "PageNumber": "1"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Domains struct {
			PageData []struct {
				DomainName   string `json:"DomainName"`
				DomainStatus string `json:"DomainStatus"`
			} `json:"PageData"`
		} `json:"Domains"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}

	total := len(r.Domains.PageData)
	s.emit("cdn", stageTitle("cdn"),
		fmt.Sprintf("发现 %d 个加速域名，开始 TLS 实测…", total), "info", 0, total)

	var (
		mu      sync.Mutex
		out     []store.CertEntry
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
		scanned int32
	)
	for _, d := range r.Domains.PageData {
		wg.Add(1)
		go func(domain, status string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			e := store.CertEntry{
				Source: "cdn",
				Name:   domain,
				Region: "global",
				Note:   "CDN(" + status + ")",
			}
			if na, issuer, ok := TLSCertProbe(domain, 10_000_000_000); ok {
				e.NotAfter = FmtDate(na)
				e.Days = DaysLeft(na)
				e.Issuer = issuer
			} else {
				e.Note += "，未开 HTTPS"
			}
			mu.Lock()
			out = append(out, e)
			mu.Unlock()

			n := int(atomic.AddInt32(&scanned, 1))
			if n%5 == 0 || n == total {
				s.emit("cdn", stageTitle("cdn"),
					fmt.Sprintf("TLS 实测 %d/%d：%s", n, total, domain), "info", n, total)
			}
		}(d.DomainName, d.DomainStatus)
	}
	wg.Wait()

	// 结果按域名排序，避免并发导致的乱序
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// scanDNS 阿里云 DNS 解析的域名（DNS-01 可用域名池）
func (s *Scanner) scanDNS() ([]store.CertEntry, error) {
	s.emit("dns", stageTitle("dns"), "正在查询云解析域名…", "info", 0, 1)

	raw, err := s.cl.RPCCall("alidns.aliyuncs.com", "2015-01-09", "DescribeDomains",
		map[string]string{"PageSize": "100", "PageNumber": "1"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Domains struct {
			TotalCount int `json:"TotalCount"`
			Domain     []struct {
				DomainName  string `json:"DomainName"`
				RecordCount int64  `json:"RecordCount"`
				PunyCode    string `json:"PunyCode"`
			} `json:"Domain"`
		} `json:"Domains"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var out []store.CertEntry
	for _, d := range r.Domains.Domain {
		out = append(out, store.CertEntry{
			Source: "dns",
			Name:   d.DomainName,
			Region: "global",
			Note:   fmt.Sprintf("解析记录 %d 条（DNS-01 可用）", d.RecordCount),
		})
	}
	s.emit("dns", stageTitle("dns"),
		fmt.Sprintf("完成，%d 个域名", len(out)), "ok", 1, 1)
	return out, nil
}

// TrimDup 去重（同名同源）
func TrimDup(in []store.CertEntry) []store.CertEntry {
	seen := map[string]bool{}
	var out []store.CertEntry
	for _, e := range in {
		k := strings.Join([]string{e.Source, e.Name, e.ID}, "|")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

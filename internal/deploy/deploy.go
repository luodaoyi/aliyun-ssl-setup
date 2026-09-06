// Package deploy 把申请到的证书部署到阿里云各服务。
// 三个目标使用不同的 API 风格：
//   - OSS  : REST + XML（PUT/POST /?cname）
//   - CDN  : RPC（cdn.aliyuncs.com，2018-05-10）
//   - SLB  : RPC（slb.<region>.aliyuncs.com，2014-05-15）
package deploy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sslpanel-app/internal/aliyun"
)

// Cert 待部署的证书内容（certID 与 PEM 二选一；有 certID 优先引用证书管家）
type Cert struct {
	Name    string
	CertID  string
	CertPEM string
	KeyPEM  string
}

// UploadToCAS 上传证书到证书管家（数字证书管理服务），返回证书 ID
func UploadToCAS(cl *aliyun.Client, c Cert) (string, error) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "sslpanel-" + time.Now().Format("20060102-150405")
	}
	if len(name) > 63 {
		name = name[:63]
	}

	raw, err := cl.RPCCall("cas.aliyuncs.com", "2020-04-07", "UploadUserCertificate", map[string]string{
		"Name": name,
		"Cert": c.CertPEM,
		"Key":  c.KeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("上传到证书管家失败: %w", err)
	}

	var r struct {
		CertId json.Number `json:"CertId"`
		Id     json.Number `json:"Id"`
	}
	_ = json.Unmarshal(raw, &r)

	if id := r.CertId.String(); id != "" && id != "0" {
		return id, nil
	}
	if id := r.Id.String(); id != "" && id != "0" {
		return id, nil
	}
	// 个别版本只回 RequestId，此时退化为直传 PEM
	return "", nil
}

// DeployOSS 更新 Bucket 自定义域名绑定的证书
func DeployOSS(cl *aliyun.Client, bucket, location, domain string, c Cert) error {
	if bucket == "" || domain == "" {
		return fmt.Errorf("OSS 部署缺少 Bucket 或域名")
	}
	return cl.OSSPutCname(bucket, location, domain, c.CertID, c.CertPEM, c.KeyPEM)
}

// DeployCDN 更新 CDN 加速域名的 HTTPS 证书
func DeployCDN(cl *aliyun.Client, domain string, c Cert) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("CDN 部署缺少域名")
	}

	if strings.TrimSpace(c.CertID) != "" {
		_, err := cl.RPCCall("cdn.aliyuncs.com", "2018-05-10", "SetCdnDomainSSLCertificate", map[string]string{
			"DomainName":  domain,
			"CertType":    "cas",
			"CertId":      c.CertID,
			"SSLProtocol": "on",
			"CertRegion":  "cn-hangzhou",
		})
		if err != nil {
			return fmt.Errorf("CDN 部署失败（%s）: %w", domain, err)
		}
		return nil
	}

	_, err := cl.RPCCall("cdn.aliyuncs.com", "2018-05-10", "SetCdnDomainSSLCertificate", map[string]string{
		"DomainName":  domain,
		"CertType":    "upload",
		"CertName":    c.Name,
		"SSLProtocol": "on",
		"SSLPub":      c.CertPEM,
		"SSLPri":      c.KeyPEM,
	})
	if err != nil {
		return fmt.Errorf("CDN 部署失败（%s）: %w", domain, err)
	}
	return nil
}

// DeploySLB 上传服务器证书并绑定到 HTTPS 监听
func DeploySLB(cl *aliyun.Client, region, lbID string, port int, c Cert) error {
	if lbID == "" || port <= 0 {
		return fmt.Errorf("SLB 部署缺少实例 ID 或监听端口")
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "sslpanel-" + time.Now().Format("20060102-150405")
	}

	raw, err := cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15", "UploadServerCertificate", map[string]string{
		"RegionId":              region,
		"ServerCertificateName": name,
		"ServerCertificate":     c.CertPEM,
		"PrivateKey":            c.KeyPEM,
	})
	if err != nil {
		return fmt.Errorf("上传 SLB 证书失败: %w", err)
	}

	var r struct {
		ServerCertificateId string `json:"ServerCertificateId"`
		ServerCertificateID string `json:"ServerCertificateID"`
	}
	_ = json.Unmarshal(raw, &r)
	certID := r.ServerCertificateId
	if certID == "" {
		certID = r.ServerCertificateID
	}
	if certID == "" {
		return fmt.Errorf("上传 SLB 证书未返回证书 ID")
	}

	_, err = cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15",
		"SetLoadBalancerHTTPSListenerAttribute", map[string]string{
			"RegionId":            region,
			"LoadBalancerId":      lbID,
			"ListenerPort":        strconv.Itoa(port),
			"ServerCertificateId": certID,
		})
	if err != nil {
		return fmt.Errorf("绑定 SLB 监听失败（%s:%d）: %w", lbID, port, err)
	}
	return nil
}

// ListenerInfo SLB HTTPS 监听信息，供前端选择部署目标
type ListenerInfo struct {
	LoadBalancerID string `json:"load_balancer_id"`
	LoadBalancer   string `json:"load_balancer_name"`
	Port           int    `json:"port"`
	CertID         string `json:"cert_id"`
	CertName       string `json:"cert_name"`
	Region         string `json:"region"`
	Address        string `json:"address"`
}

// ListSLBHTTPSListeners 列出某地域全部 HTTPS 监听及其当前证书
func ListSLBHTTPSListeners(cl *aliyun.Client, region string) ([]ListenerInfo, error) {
	raw, err := cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15", "DescribeLoadBalancers",
		map[string]string{"RegionId": region})
	if err != nil {
		return nil, fmt.Errorf("查询 SLB 实例失败（%s）: %w", region, err)
	}

	var lbs struct {
		LoadBalancers struct {
			LoadBalancer []struct {
				LoadBalancerID   string `json:"LoadBalancerId"`
				LoadBalancerName string `json:"LoadBalancerName"`
				Address          string `json:"Address"`
			} `json:"LoadBalancer"`
		} `json:"LoadBalancers"`
	}
	if err := json.Unmarshal(raw, &lbs); err != nil {
		return nil, fmt.Errorf("解析 SLB 实例列表失败: %w", err)
	}

	var out []ListenerInfo
	for _, lb := range lbs.LoadBalancers.LoadBalancer {
		lraw, err := cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15",
			"DescribeLoadBalancerHTTPSListenerAttribute", map[string]string{
				"RegionId":       region,
				"LoadBalancerId": lb.LoadBalancerID,
				"ListenerPort":   "443",
			})
		if err != nil {
			continue
		}
		var l struct {
			ListenerPort       int    `json:"ListenerPort"`
			ServerCertificateId string `json:"ServerCertificateId"`
			Status             string `json:"Status"`
		}
		if err := json.Unmarshal(lraw, &l); err != nil {
			continue
		}
		if l.ListenerPort == 0 {
			continue
		}

		// 反查证书名称
		certName := ""
		if l.ServerCertificateId != "" {
			if craw, err := cl.RPCCall("slb."+region+".aliyuncs.com", "2014-05-15",
				"DescribeServerCertificates", map[string]string{"RegionId": region}); err == nil {
				var cs struct {
					ServerCertificates struct {
						ServerCertificate []struct {
							ServerCertificateID   string `json:"ServerCertificateId"`
							ServerCertificateName string `json:"ServerCertificateName"`
						} `json:"ServerCertificate"`
					} `json:"ServerCertificates"`
				}
				if err := json.Unmarshal(craw, &cs); err == nil {
					for _, sc := range cs.ServerCertificates.ServerCertificate {
						if sc.ServerCertificateID == l.ServerCertificateId {
							certName = sc.ServerCertificateName
							break
						}
					}
				}
			}
		}

		out = append(out, ListenerInfo{
			LoadBalancerID: lb.LoadBalancerID,
			LoadBalancer:   lb.LoadBalancerName,
			Port:           l.ListenerPort,
			CertID:         l.ServerCertificateId,
			CertName:       certName,
			Region:         region,
			Address:        lb.Address,
		})
	}
	return out, nil
}

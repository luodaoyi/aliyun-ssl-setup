// Package aliyun 实现阿里云 RPC 风格 API 调用（HMAC-SHA1 签名）与 OSS REST API，
// 全部使用 Go 标准库，零外部依赖。
package aliyun

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	AKID   string
	Secret string
	HTTP   *http.Client
}

func NewClient(akid, secret string) *Client {
	return &Client{
		AKID:   akid,
		Secret: secret,
		HTTP:   &http.Client{Timeout: 25 * time.Second},
	}
}

// pctEncode 阿里云 RPC 签名要求的 RFC3986 百分号编码（+ 为 %20，* 为 %2A，~ 保留）
func pctEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
		}
	}
	return b.String()
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// RPCCall 调用 RPC 风格 API（GET，HMAC-SHA1 签名，JSON 格式响应）
func (c *Client) RPCCall(endpoint, version, action string, params map[string]string) (json.RawMessage, error) {
	p := map[string]string{
		"Action":           action,
		"Format":           "JSON",
		"Version":          version,
		"AccessKeyId":      c.AKID,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   nonce(),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range params {
		p[k] = v
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var q strings.Builder
	for i, k := range keys {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(pctEncode(k))
		q.WriteByte('=')
		q.WriteString(pctEncode(p[k]))
	}
	sts := "GET&" + pctEncode("/") + "&" + pctEncode(q.String())
	mac := hmac.New(sha1.New, []byte(c.Secret+"&"))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	url := "https://" + strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://") +
		"?" + q.String() + "&Signature=" + pctEncode(sig)

	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", action, resp.StatusCode, truncate(string(body), 300))
	}
	return json.RawMessage(body), nil
}

// ---------- 日期解析（兼容 epoch 毫秒/秒、ISO8601、GMT、YYYY-MM-DD） ----------

// ParseEpoch 兼容秒/毫秒 epoch（json.Number 或字符串）
func ParseEpoch(v json.Number) (time.Time, bool) {
	s := v.String()
	if s == "" || s == "0" {
		return time.Time{}, false
	}
	n, err := v.Int64()
	if err != nil {
		return time.Time{}, false
	}
	// 大于 1e12 视为毫秒
	var sec int64
	switch {
	case n > 1e15: // 纳秒
		sec = n / 1e9
	case n > 1e12: // 毫秒
		sec = n / 1e3
	case n > 1e9: // 秒
		sec = n
	default:
		// 可能是 YYYYMMDD 或年份，按无效处理
		return time.Time{}, false
	}
	return time.Unix(sec, 0).UTC(), true
}

var gmtLayouts = []string{
	"Jan _2 15:04:05 2006 MST",  // Mar 12 23:59:59 2027 GMT（OSS ValidEndDate）
	"2006-01-02T15:04:05Z07:00", // ISO8601
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ParseDateStr 解析多种格式的日期字符串
func ParseDateStr(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range gmtLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// ---------- OSS REST API ----------

type OSSBucket struct {
	Name     string `xml:"Name"`
	Location string `xml:"Location"`
}

type OSSCert struct {
	Type         string `xml:"Type"`
	CertID       string `xml:"CertId"`
	Status       string `xml:"Status"`
	ValidEndDate string `xml:"ValidEndDate"`
}

type OSSCname struct {
	Domain      string   `xml:"Domain"`
	Status      string   `xml:"Status"`
	Certificate *OSSCert `xml:"Certificate"`
}

type CnameResult struct {
	XMLName xml.Name   `xml:"ListCnameResult"`
	Cnames  []OSSCname `xml:"Cname"`
}

// ossGET 对 OSS 发起签名的 GET 请求
// 虚拟主机风格：urlPath 是 URL 路径（如 "/?cname"），canonicalResource 是参与签名的
// 规范化资源（如 "/bucket/?cname"），两者不同，必须分开传
func (c *Client) ossGET(host, urlPath, canonicalResource string) ([]byte, error) {
	date := time.Now().UTC().Format(http.TimeFormat)
	sts := "GET\n\n\n" + date + "\n" + canonicalResource
	mac := hmac.New(sha1.New, []byte(c.Secret))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodGet, "https://"+host+urlPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", "OSS "+c.AKID+":"+sig)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		// 无 CNAME 配置时返回 404/NoSuchCname，属正常
		return nil, fmt.Errorf("oss %s: HTTP %d: %s", host, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// OSSListBuckets 列出账号全部 Bucket（GetService）
func (c *Client) OSSListBuckets() ([]OSSBucket, error) {
	body, err := c.ossGET("oss-cn-hangzhou.aliyuncs.com", "/", "/")
	if err != nil {
		return nil, err
	}
	var r struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Buckets struct {
			Bucket []OSSBucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := xml.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 Bucket 列表: %w", err)
	}
	return r.Buckets.Bucket, nil
}

// OSSGetCname 查询 Bucket 的自定义域名（及托管证书信息）
func (c *Client) OSSGetCname(bucket, location string) (*CnameResult, error) {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = "oss-cn-hangzhou"
	}
	if !strings.HasPrefix(loc, "oss-") {
		loc = "oss-" + loc
	}
	host := bucket + "." + loc + ".aliyuncs.com"
	body, err := c.ossGET(host, "/?cname", "/"+bucket+"/?cname")
	if err != nil {
		return nil, err
	}
	var r CnameResult
	if err := xml.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 CNAME: %w", err)
	}
	return &r, nil
}

// ossPOST 对 OSS 发起签名的 POST 请求（带 Content-MD5，OSS 强制要求）
// canonicalResource 形如 "/bucket/?cname&comp=add"，子资源必须参与签名且按字典序排列
func (c *Client) ossPOST(host, urlPath, canonicalResource string, body []byte) ([]byte, error) {
	date := time.Now().UTC().Format(http.TimeFormat)
	sum := md5.Sum(body)
	md5b64 := base64.StdEncoding.EncodeToString(sum[:])
	contentType := "application/xml"

	// VERB + "\n" + Content-MD5 + "\n" + Content-Type + "\n" + Date + "\n" + CanonicalizedResource
	sts := "POST\n" + md5b64 + "\n" + contentType + "\n" + date + "\n" + canonicalResource
	mac := hmac.New(sha1.New, []byte(c.Secret))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, "https://"+host+urlPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-MD5", md5b64)
	req.Header.Set("Authorization", "OSS "+c.AKID+":"+sig)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("oss %s: HTTP %d: %s", host, resp.StatusCode, truncate(string(rb), 300))
	}
	return rb, nil
}

// OSSPutCname 绑定/更新 Bucket 自定义域名并挂载证书
// certID 非空时引用证书管家证书，否则直传 PEM
func (c *Client) OSSPutCname(bucket, location, domain, certID, certPEM, keyPEM string) error {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = "oss-cn-hangzhou"
	}
	if !strings.HasPrefix(loc, "oss-") {
		loc = "oss-" + loc
	}
	host := bucket + "." + loc + ".aliyuncs.com"

	var certCfg string
	if strings.TrimSpace(certID) != "" {
		certCfg = "<CertId>" + xmlEscape(certID) + "</CertId>"
	} else if strings.TrimSpace(certPEM) != "" {
		certCfg = "<Certificate>" + xmlEscape(certPEM) + "</Certificate>" +
			"<PrivateKey>" + xmlEscape(keyPEM) + "</PrivateKey>" +
			"<Force>true</Force>"
	}

	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<BucketCnameConfiguration>
  <Cname>
    <Domain>` + xmlEscape(domain) + `</Domain>` +
		func() string {
			if certCfg == "" {
				return ""
			}
			return "<CertificateConfiguration>" + certCfg + "</CertificateConfiguration>"
		}() + `
  </Cname>
</BucketCnameConfiguration>`)

	_, err := c.ossPOST(host, "/?cname&comp=add", "/"+bucket+"/?cname&comp=add", body)
	if err != nil {
		return fmt.Errorf("绑定域名 %s 失败: %w", domain, err)
	}
	return nil
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// ---------- TLS 直连探测 ----------

// TLSCertProbe 对域名 443 做握手取证书有效期（不校验信任链，只读到期时间）
func TLSCertProbe(domain string, timeout time.Duration) (notAfter time.Time, issuer string, ok bool) {
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", domain+":443", &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS10,
	})
	if err != nil {
		return time.Time{}, "", false
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, "", false
	}
	return certs[0].NotAfter.UTC(), certs[0].Issuer.CommonName, true
}

// DaysLeft 计算剩余天数
func DaysLeft(t time.Time) *int {
	if t.IsZero() {
		return nil
	}
	d := int(time.Until(t).Hours() / 24)
	return &d
}

// FmtDate 输出 YYYY-MM-DD
func FmtDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// SansToList 拆分 SAN 字符串
func SansToList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseJSONNumber 把 any 转成 json.Number（CAS 等接口字段可能是数字或字符串）
func ParseJSONNumber(v any) json.Number {
	switch x := v.(type) {
	case json.Number:
		return x
	case string:
		return json.Number(x)
	case float64:
		return json.Number(strconv.FormatFloat(x, 'f', -1, 64))
	}
	return json.Number("")
}

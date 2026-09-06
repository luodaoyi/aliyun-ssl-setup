package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CertEntry 一条证书/域名库存记录
type CertEntry struct {
	Source    string   `json:"source"`              // cas / slb / oss / cdn / dns
	Name      string   `json:"name"`                // 证书名 / 域名
	Domains   []string `json:"domains,omitempty"`   // 覆盖的域名（SAN 等）
	Issuer    string   `json:"issuer,omitempty"`
	NotBefore string   `json:"not_before,omitempty"` // YYYY-MM-DD
	NotAfter  string   `json:"not_after,omitempty"`  // YYYY-MM-DD
	Days      *int     `json:"days,omitempty"`       // 剩余天数（无到期信息为 nil）
	Region    string   `json:"region,omitempty"`
	ID        string   `json:"id,omitempty"`         // 资源 ID（CertId / ServerCertificateId 等）
	Bucket    string   `json:"bucket,omitempty"`     // OSS Bucket 名（仅 oss 来源，部署时用）
	Note      string   `json:"note,omitempty"`       // 备注（如“未托管证书，TLS 实测”）
}

// IssuedCert 本地已签发的证书记录
type IssuedCert struct {
	Key       string   `json:"key"`        // 主域名（去通配符），用作唯一标识
	Domains   []string `json:"domains"`
	CertPath  string   `json:"cert_path"`
	KeyPath   string   `json:"key_path"`
	NotAfter  string   `json:"not_after"`
	Days      int      `json:"days"`
	Issuer    string   `json:"issuer"`
	CA        string   `json:"ca"`
	CertID    string   `json:"cert_id,omitempty"` // 上传到证书管家后的 ID
	CreatedAt string   `json:"created_at"`
}

// ScanResult 一次扫描的结果快照
type ScanResult struct {
	Time    time.Time   `json:"time"`
	Certs   []CertEntry `json:"certs"`
	Errors  []string    `json:"errors,omitempty"`
}

// Store JSON 文件状态存储（零依赖，纯标准库）
type Store struct {
	mu     sync.Mutex
	path   string
	Result *ScanResult
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "state.json")}
	s.load()
	return s, nil
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var r ScanResult
	if json.Unmarshal(data, &r) == nil {
		s.Result = &r
	}
}

func (s *Store) Set(r *ScanResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Result = r
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0o600)
}

func (s *Store) Get() *ScanResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Result
}

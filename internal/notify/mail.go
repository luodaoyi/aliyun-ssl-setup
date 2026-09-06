// Package notify 提供邮件通知能力（无人值守续期的结果汇总）。
package notify

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Config 邮件发送配置（来自 config.json）
type Config struct {
	Host string // SMTP 服务器
	Port int    // 465 = 隐式 SSL，其余端口走 STARTTLS
	User string // 登录账号（通常为发件邮箱）
	Pass string // 授权码/密码
	To   string // 收件人，多个用逗号或分号分隔
	From string // 发件人显示地址，留空则取 User
}

// Enabled 是否已具备发信条件
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.Host) != "" &&
		strings.TrimSpace(c.User) != "" &&
		strings.TrimSpace(c.To) != ""
}

// Send 发送纯文本邮件
func Send(cfg Config, subject, body string) error {
	if !cfg.Enabled() {
		return fmt.Errorf("SMTP 未配置（host/user/to 任一为空）")
	}
	port := cfg.Port
	if port == 0 {
		port = 465
	}
	from := strings.TrimSpace(cfg.From)
	if from == "" {
		from = strings.TrimSpace(cfg.User)
	}
	toList := splitAddrs(cfg.To)
	if len(toList) == 0 {
		return fmt.Errorf("收件人为空")
	}

	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\nDate: %s\r\n\r\n%s",
		from, strings.Join(toList, ", "), encodeSubject(subject),
		time.Now().Format(time.RFC1123Z), body))

	addr := fmt.Sprintf("%s:%d", strings.TrimSpace(cfg.Host), port)
	host := strings.TrimSpace(cfg.Host)
	auth := smtp.PlainAuth("", strings.TrimSpace(cfg.User), cfg.Pass, host)

	var cli *smtp.Client
	if port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("SSL 连接失败: %w", err)
		}
		cli, err = smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP 握手失败: %w", err)
		}
	} else {
		conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		cli, err = smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP 握手失败: %w", err)
		}
		if ok, _ := cli.Extension("STARTTLS"); ok {
			if err := cli.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("STARTTLS 失败: %w", err)
			}
		}
	}
	defer func() { _ = cli.Quit() }()

	if auth != nil {
		if err := cli.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := cli.Mail(from); err != nil {
		return fmt.Errorf("发件人被拒绝: %w", err)
	}
	for _, t := range toList {
		if err := cli.Rcpt(t); err != nil {
			return fmt.Errorf("收件人被拒绝（%s）: %w", t, err)
		}
	}
	w, err := cli.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("写入正文失败: %w", err)
	}
	return w.Close()
}

func splitAddrs(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n'
	}) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// encodeSubject 用 Base64 编码主题，避免中文乱码
func encodeSubject(s string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

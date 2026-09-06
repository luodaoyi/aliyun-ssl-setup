// 无头进度探针：真实调用阿里云只读 API，把 Scanner 的进度事件流打印出来。
// 用于在无法启动 WebView2 的环境下验证「实时进度」后端链路。
//
// 用法（在 sslpanel-app 目录下）：
//   go run ./tools/progress_probe
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"sslpanel-app/internal/aliyun"
)

type conf struct {
	AccessKeyID     string   `json:"access_key_id"`
	AccessKeySecret string   `json:"access_key_secret"`
	Regions         []string `json:"regions"`
}

func main() {
	cfgPath := "build/bin/config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Println("读取配置失败:", err)
		os.Exit(1)
	}
	var c conf
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Println("解析配置失败:", err)
		os.Exit(1)
	}
	if len(c.Regions) == 0 {
		c.Regions = []string{"cn-beijing", "cn-hangzhou"}
	}

	cl := aliyun.NewClient(c.AccessKeyID, c.AccessKeySecret)
	sc := aliyun.NewScanner(cl, c.Regions)

	start := time.Now()
	n := 0
	sc.OnProgress = func(p aliyun.Progress) {
		n++
		fmt.Printf("[%6.2fs] #%-3d %-5s | %-16s | %3d%% | %-4s | %s\n",
			time.Since(start).Seconds(), n, p.Stage, p.Title, p.Percent,
			defaultStr(p.Level, "info"), p.Detail)
	}

	fmt.Println("== 开始扫描 ==")
	certs, errs := sc.ScanAll()
	fmt.Printf("== 结束：%d 条证书，%d 个错误，%.1fs，共 %d 条进度事件 ==\n",
		len(certs), len(errs), time.Since(start).Seconds(), n)
	for _, e := range errs {
		if strings.TrimSpace(e) != "" {
			fmt.Println("  ERR:", e)
		}
	}
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

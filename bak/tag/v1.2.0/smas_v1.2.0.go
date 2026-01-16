package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 服务器监控指标的配置结构体
type Server struct {
	Addresses       []string `json:"addresses"`
	Ports           []string `json:"port"`
	Processes       []string `json:"processes"`       // 支持多个进程
	CPUThreshold    float64  `json:"cpuThreshold"`    // CPU利用率告警阈值
	MemoryThreshold float64  `json:"memoryThreshold"` // 内存利用率告警阈值
	DiskThreshold   float64  `json:"diskThreshold"`   // 磁盘利用率告警阈值
	//FolderPath         float64  `json:"folder_path"`
	ExcludeMountPoints []string `json:"excludeMountPoints"` // 排除的磁盘挂载点
}

// 邮件配置的结构体
type EmailConfig struct {
	From       string   `json:"from"`
	Password   string   `json:"password"`
	SMTPHost   string   `json:"smtpHost"`
	SMTPPort   string   `json:"smtpPort"`
	Recipients []string `json:"recipients"`
}

// 配置文件的总结构
type Config struct {
	Email   EmailConfig   `json:"email"`
	Servers []Server      `json:"servers"`
	Monitor MonitorConfig `json:"monitor"` // 全局监控配置
}

// 服务器告警发送状态的结构体
type ServerStatus struct {
	PortAlertSent       bool
	ProcessAlertSent    map[string]bool // 记录每个进程的告警状态
	CpuAlertSent        bool
	MemAlertSent        bool
	DiskAlertSent       map[string]bool
	DirAlertSent        map[string]bool
	FileAlertSent       map[string]bool
	PingAlertSent       bool
	TargetPortAlertSent map[string]bool // 记录每个目标服务器端口的告警状态
}

// StatusResponse 表示 /check 接口的响应
type StatusResponse struct {
	DirectoryStatuses []DirectoryStatus `json:"directoryStatuses"`
	ProcessStatuses   []ProcessStatus   `json:"processStatuses"`
	Metrics           Metrics           `json:"metrics"`
	PortStatuses      []PortStatus      `json:"portStatuses"`
}

// 进程状态的结构体
type ProcessStatus struct {
	ProcessName string `json:"processName"`
	IsRunning   bool   `json:"isRunning"`
}

// DirectoryResponse 表示 /check 接口的响应
type DirectoryStatus struct {
	DirectoryExist bool   `json:"directoryExist"`
	XdrFileExist   bool   `json:"xdrfileExist"`
	BaseDir        string `json:"baseDir"`
}

// 服务器资源使用情况的结构体
type Metrics struct {
	CPUUsage    float64            `json:"cpu_usage"`
	MemoryUsage float64            `json:"memory_usage"`
	DiskUsage   map[string]float64 `json:"disk_usage"`
}

type PortStatus struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Status bool   `json:"status"` // "true" or "false"
}

// 邮件模板数据结构体 (针对服务器监控)
type EmailTemplateData struct {
	Subject   string // 告警主题
	Server    string // 服务器地址
	Message   string // 告警详细信息
	Value     string // 当前值（如使用率）
	Threshold string // 阈值
	Action    string // 建议操作
	Timestamp string // 时间戳
}

// 新增全局监控配置结构体
type MonitorConfig struct {
	ProcessMonitor     bool `json:"process"`  // 是否监控进程
	PortMonitor        bool `json:"port"`     // 是否监控端口
	ServerReachMonitor bool `json:"ping"`     // 是否监控服务器通信状态
	Resource           bool `json:"resource"` // 是否监控资源
	DirFileMonitor     bool `json:"dir"`      // 是否监控目录
	RaPortMonitor      bool `json:"raport"`   // 是否监控被监测服务器和目标服务器端口连通性
}

// 全局状态变量，记录每个服务器的状态
var (
	statusesMutex sync.Mutex // 全局状态锁
	templates     *template.Template
)

// 主函数
func main() {
	// 获取项目文件夹路径
	projectRoot, err := getProjectRoot()
	if err != nil {
		fmt.Printf("Error determining project root: %v\n", err)
		return
	}
	templatesDir := filepath.Join(projectRoot, "templates")
	if err := initTemplates(templatesDir); err != nil {
		log.Fatalf("Failed to initialize email templates: %v", err)
	}
	if err != nil {
		fmt.Printf("Error determining project root: %v\n", err)
		return
	}
	// 获取项目文件夹下conf目录下的config.json路径
	configPath = filepath.Join(projectRoot, "conf", "config.json")

	// 初始加载配置
	if err := reloadConfig(); err != nil {
		log.Fatalf("Failed to load initial configuration: %v", err)
	}

	go startConfigWatcher()

	// 初始化全局状态变量
	statuses := make(map[string]*ServerStatus)
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			for _, port := range server.Ports { // 遍历所有端口
				//address := server.Address
				//port := server.Port
				key := fmt.Sprintf("%s:%s", address, port)

				// 为每个服务器的告警状态初始化 ServerStatus 结构体
				statuses[key] = &ServerStatus{
					PortAlertSent:       false,
					ProcessAlertSent:    make(map[string]bool), // 初始化为非空 map
					CpuAlertSent:        false,
					MemAlertSent:        false,
					DiskAlertSent:       make(map[string]bool), // 初始化为非空 map
					DirAlertSent:        make(map[string]bool),
					FileAlertSent:       make(map[string]bool),
					PingAlertSent:       false,
					TargetPortAlertSent: make(map[string]bool),
				}
			}
		}
	}
	// 启动各个监控任务的 goroutine
	for {
		// 启动监控进程
		go func() {
			for {
				monitorServersProcess(config, statuses)
				// 每 5 秒检查一次
				time.Sleep(3 * time.Second)
			}
		}()
		// 启动监控被监测服务器和目标服务器端口连通性
		go func() {
			for {
				monitorTargetPort(config, statuses)
				// 每 5 秒检查一次
				time.Sleep(30 * time.Second)
			}
		}()
		// 启动监控服务器通信状态
		go func() {
			for {
				monitorServersState(config, statuses)
				// 每 60 秒检查一次
				time.Sleep(60 * time.Second)
			}
		}()
		// 启动监控端口
		go func() {
			for {
				monitorServerPorts(config, statuses)
				// 每 10 秒检查一次
				time.Sleep(10 * time.Second)
			}
		}()

		// 启动监控资源
		go func() {
			for {
				monitorResources(config, statuses)
				// 每 180 秒检查一次
				time.Sleep(180 * time.Second)
			}
		}()

		// 启动监控目录
		go func() {
			for {
				currentMinute := time.Now().Minute()
				// 判断当前时间的分钟数是否是整5分钟后的后两分钟
				if currentMinute%5 == 2 {
					monitorDirectory(config, statuses)
				}
				// 每 30 秒检查一次
				time.Sleep(30 * time.Second)
			}
		}()

		// 阻塞主线程，保持程序运行
		select {}
	}
}

// 解析地址范围，支持单个IP和IP范围
func parseAddresses(addresses []string) ([]string, error) {
	var result []string

	for _, addr := range addresses {
		// 检查是否是范围格式
		if strings.Contains(addr, "-") {
			parts := strings.Split(addr, ".")
			if len(parts) != 4 {
				return nil, fmt.Errorf("invalid IP range format: %s", addr)
			}

			lastPart := parts[3]
			if strings.Contains(lastPart, "-") {
				rangeParts := strings.Split(lastPart, "-")
				if len(rangeParts) != 2 {
					return nil, fmt.Errorf("invalid IP range format: %s", addr)
				}

				start, err1 := strconv.Atoi(rangeParts[0])
				end, err2 := strconv.Atoi(rangeParts[1])
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("invalid IP range numbers: %s", addr)
				}

				for i := start; i <= end; i++ {
					ip := fmt.Sprintf("%s.%s.%s.%d", parts[0], parts[1], parts[2], i)
					result = append(result, ip)
				}
			} else {
				// 不是范围格式，直接添加
				result = append(result, addr)
			}
		} else {
			// 不是范围格式，直接添加
			result = append(result, addr)
		}
	}

	return result, nil
}

// 获取项目根目录（bin 目录的上一级）
func getProjectRoot() (string, error) {
	exePath, err := os.Executable() // 获取当前可执行文件路径
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(exePath)), nil // 返回 bin 的上一级目录
}

func initTemplates(templatesDir string) error {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
	}).ParseGlob(filepath.Join(templatesDir, "*.html"))
	if err != nil {
		return err
	}
	templates = tmpl
	return nil
}

// 检查端口状态，支持 TCP 和 UDP
func checkPort(address, port string) bool {
	// 检查TCP端口
	tcpConn, tcpErr := net.DialTimeout("tcp", net.JoinHostPort(address, port), 3*time.Second)
	if tcpErr == nil {
		tcpConn.Close()
		return true
	}
	// 检查UDP端口
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(address, port))
	if err != nil {
		return false
	}

	// 增加重试机制
	const maxRetries = 2
	for i := 0; i < maxRetries; i++ {
		if checkUDP(udpAddr) {
			return true
		}
		time.Sleep(100 * time.Millisecond) // 每次重试间隔
	}

	// 如果重试次数用尽仍未成功，则认为端口关闭
	return false
}

// 检查 UDP 端口状态，发送测试数据包来测试返回连通性
func checkUDP(udpAddr *net.UDPAddr) bool {
	// 创建 UDP 连接是否成功
	udpConn, udpErr := net.DialUDP("udp", nil, udpAddr)
	if udpErr != nil {
		return false
	}
	defer udpConn.Close()

	// 发送测试数据包
	_, writeErr := udpConn.Write([]byte("test"))
	if writeErr != nil {
		return false
	}

	// 设置读取超时时间20s
	udpConn.SetReadDeadline(time.Now().Add(20 * time.Second))

	// 创建一个512字节大小的缓冲区，接收UDP套接字读取的数据
	buf := make([]byte, 512)
	_, _, readErr := udpConn.ReadFrom(buf) //读取数据填入buf中，返回的是三个值，字节数、远端地址、错误信息
	if readErr != nil {
		// 如果超时错误直接返回 false，其他错误可以ok
		if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
			return false
		}
		return false
	}

	// 收到响应，认为端口开放
	return true
}

// 检查服务器通信状态，是否可以ping通
func checkPingState(address string) bool {
	const maxRetries = 3
	const waitBetweenRetries = 3 * time.Second

	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			time.Sleep(waitBetweenRetries)
			fmt.Printf("地址 %s 检测失败，第 %d 次重试...\n", address, retry)
		}

		// 执行ping命令
		cmd := exec.Command("ping", "-c", "2", "-W", "5", address)
		output, err := cmd.CombinedOutput()
		outputStr := string(output)

		if err != nil {
			// 命令执行失败（如超时）
			continue
		}

		// 解析输出，检查是否有来自目标地址的响应
		if hasValidResponse(outputStr, address) {
			return true
		}
	}

	return false
}

// 解析ping输出，检查是否有来自目标地址的响应
func hasValidResponse(output, targetAddr string) bool {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		// 跳过空行和统计行
		if strings.TrimSpace(line) == "" || strings.Contains(line, "packets transmitted") {
			continue
		}

		// 尝试提取源IP地址
		if srcIP := extractSourceIP(line); srcIP != "" {
			// 调试信息
			//fmt.Printf("检测到来自 %s 的响应 (目标: %s)\n", srcIP, targetAddr)

			// 检查是否是目标地址
			if srcIP == targetAddr {
				return true
			}
		}
	}
	return false
}

// 从ping输出行中提取源IP地址
func extractSourceIP(line string) string {
	// 尝试匹配不同格式的输出
	patterns := []*regexp.Regexp{
		// 中文输出格式: "64 字节，来自 10.45.14.160: icmp_seq=1 ttl=58 时间=5.48 毫秒"
		regexp.MustCompile(`来自 (\d+\.\d+\.\d+\.\d+)`),
		// 英文输出格式: "64 bytes from 10.45.14.160: icmp_seq=1 ttl=58 time=5.48 ms"
		regexp.MustCompile(`from (\d+\.\d+\.\d+\.\d+)`),
		// 其他可能格式
		regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+).*icmp_seq`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(line)
		if len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// 辅助函数：检查挂载点是否在排除列表中
func isExcludedMountPoint(mountpoint string, excludePatterns []string) bool {
	for _, pattern := range excludePatterns {
		// 如果模式是精确匹配
		if pattern == mountpoint {
			return true
		}

		// 如果模式包含通配符，使用正则匹配
		if strings.Contains(pattern, "*") || strings.Contains(pattern, ".*") {
			matched, err := regexp.MatchString(pattern, mountpoint)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

// 发送邮件函数（两个变量：邮件配置结构体声明的对象、主题、邮件内容）
func sendEmail(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	if data.Timestamp == "" {
		data.Timestamp = time.Now().Format("2006-01-02 15:04:05")
	}

	// 根据告警级别选择模板
	templateName := ""
	switch alertLevel {
	case "critical":
		templateName = "critical_alert.html"
	case "severe":
		templateName = "severe_alert.html"
	case "warning":
		templateName = "warning_alert.html"
	case "recovery":
		templateName = "recovery_alert.html"
	default:
		log.Printf("Unknown alert level: %s", alertLevel)
		return
	}

	// 渲染模板
	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, templateName, data); err != nil {
		log.Printf("Failed to execute template: %v", err)
		return
	}
	// 获取配置文件的邮件配置相关信息
	from := emailConfig.From
	to := strings.Join(emailConfig.Recipients, ",")
	smtpHost := emailConfig.SMTPHost
	smtpPort := emailConfig.SMTPPort

	// 构建邮件内容
	//var bodyBuffer bytes.Buffer // 用于存储和操作字节数据
	var emailContent bytes.Buffer
	//subject := "Your Email Subject" // Define the subject variable
	encodedSubject := mime.QEncoding.Encode("utf-8", data.Subject)
	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   encodedSubject,
		"MIME-Version":              "1.0",
		"Content-Type":              "text/html; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
	}

	for k, v := range headers {
		fmt.Fprintf(&emailContent, "%s: %s\r\n", k, v) // 将headers写入emailContent
	}
	fmt.Fprintf(&emailContent, "\r\n")
	emailContent.Write(body.Bytes()) // 写入HTML内容

	// 创建TLS配置，跳过证书验证
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // 忽略证书验证
	}

	// 建立与SMTP服务器的TLS连接
	conn, err := tls.Dial("tcp", smtpHost+":"+smtpPort, tlsConfig)
	if err != nil {
		log.Fatal("无法连接到SMTP服务器:", err)
	}

	// 创建SMTP客户端，下面的身份验证、发送邮件等操作都通过这个客户端进行
	client, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		log.Fatal("创建SMTP客户端失败:", err)
	}
	// 进行身份验证
	auth := smtp.PlainAuth("", from, emailConfig.Password, smtpHost) // 一般第一个空的是username，有些邮箱不需要
	if err := client.Auth(auth); err != nil {
		log.Fatal("身份验证失败:", err)
	}

	// 设置发件人和收件人
	err = client.Mail(from)
	if err != nil {
		log.Fatal("设置发件人失败:", err)
	}
	for _, recipient := range emailConfig.Recipients {
		err = client.Rcpt(recipient)
		if err != nil {
			log.Fatal("设置收件人失败:", err)
		}
	}

	// 获取邮件数据流
	wc, err := client.Data()
	if err != nil {
		log.Fatal("获取邮件数据流失败:", err)
	}
	// 将bodybuffer的内容写入邮件数据流
	_, err = wc.Write(emailContent.Bytes())
	if err != nil {
		log.Fatal("写入邮件数据失败:", err)
	}

	// 关闭邮件数据流
	err = wc.Close()
	if err != nil {
		log.Fatal("关闭邮件数据流失败:", err)
	}

	// 退出SMTP会话
	err = client.Quit()
	if err != nil {
		log.Fatal("退出SMTP连接失败:", err)
	}

	log.Println("邮件发送成功")
}

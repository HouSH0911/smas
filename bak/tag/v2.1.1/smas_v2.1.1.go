package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Email                EmailConfig   `json:"email"`
	Servers              []Server      `json:"servers"`
	Monitor              MonitorConfig `json:"monitor"`              // 全局监控配置
	EmailRateLimit       int           `json:"emailRateLimit"`       // 新增邮件发送频率限制字段
	EnableEmail          bool          `json:"enableEmail"`          // 是否启用邮件发送
	ResourceSmooth       float64       `json:"resourceEma"`          // 资源监控平滑指数
	ConsecutiveToAlert   int           `json:"consecutiveToAlert"`   // 连续多少次超过阈值后告警
	ConsecutiveToRecover int           `json:"consecutiveToRecover"` // 连续多少次低于阈值后恢复告警
}

// 服务器告警发送状态的结构体
type ServerStatus struct {
	PortAlertSent    int32
	ProcessAlertSent map[string]int32 // 记录每个进程的告警状态
	ProcessMutex     sync.Mutex       // 添加互斥锁字段

	CpuAlertSent int32
	MemAlertSent int32

	DiskAlertSent map[string]int32
	DiskMutex     sync.Mutex // 添加互斥锁字段

	DirAlertSent  map[string]int32
	FileAlertSent map[string]int32
	DirMutex      sync.Mutex // 添加互斥锁字段

	PingAlertSent int32

	TargetPortMutex     sync.Mutex       // 添加互斥锁字段
	TargetPortAlertSent map[string]int32 // 记录每个目标服务器端口的告警状态
	// 去抖与平滑字段（新增）
	ResourceMutex  sync.Mutex // 资源告警相关互斥
	CpuHighCount   int        // 连续高于阈值计数
	CpuNormalCount int        // 连续低于阈值计数（用于恢复）
	LastCPUEMA     float64    // 上一次的指数平滑值（初始 0 表示未初始化）

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

type serverJob struct {
	address string
	port    string
	server  Server
	key     string
}

// 邮件队列结构
type EmailQueue struct {
	queue chan emailTask
	wg    sync.WaitGroup
}

type emailTask struct {
	emailConfig EmailConfig
	alertLevel  string
	data        EmailTemplateData
}

// 全局状态变量，记录每个服务器的状态
var (
	statusesMutex  sync.Mutex // 全局状态锁
	templates      *template.Template
	httpClient     *http.Client
	cachedChecks   sync.Map          // address -> *StatusResponse
	lastCheckTime  sync.Map          // address -> time.Time
	smtpClientPool chan *smtp.Client // SMTP连接池
	//smtpMutex      sync.Mutex        // SMTP连接池锁
	emailQueue        *EmailQueue
	pingRunning       atomic.Bool
	portRunning       atomic.Bool
	processRunning    atomic.Bool
	resourceRunning   atomic.Bool
	directoryRunning  atomic.Bool
	targetPortRunning atomic.Bool
)

const (
	maxWorkers        = 100 // 最大工作协程数
	checkCacheTTL     = 10  // 检查结果缓存时间(秒)
	resourceCheckFreq = 180 // 资源监控频率(秒)
	portCheckFreq     = 6   // 端口监控频率(秒)
	pingCheckFreq     = 60  // Ping监控频率(秒)
	processCheckFreq  = 3   // 进程监控频率(秒)
	targetPortFreq    = 30  // 目标端口监控频率(秒)
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

	// 初始化邮件系统 (在加载配置后)
	initSMTPPool(config.Email, 5) // 连接池大小5
	initEmailQueue(3)             // 3个邮件发送协程

	defer emailQueue.Close()

	// 创建不同频率的 Ticker
	portTicker := time.NewTicker(time.Duration(portCheckFreq) * time.Second)
	processTicker := time.NewTicker(time.Duration(processCheckFreq) * time.Second)
	resourceTicker := time.NewTicker(time.Duration(resourceCheckFreq) * time.Second)
	pingTicker := time.NewTicker(time.Duration(pingCheckFreq) * time.Second)
	//dirTicker := time.NewTicker(time.Duration(dirCheckFreq) * time.Second)
	targetPortTicker := time.NewTicker(time.Duration(targetPortFreq) * time.Second)

	defer portTicker.Stop()
	defer processTicker.Stop()
	defer resourceTicker.Stop()
	defer pingTicker.Stop()
	//defer dirTicker.Stop()
	defer targetPortTicker.Stop()
	// 立即执行一次所有检查作为启动
	// log.Println("===== Initial monitoring run START =====")
	// go runPingChecks(config, statuses)
	// go runPortChecks(config, statuses)
	// go runProcessChecks(config, statuses)
	// go runResourceChecks(config, statuses)
	// go runDirectoryChecks(config, statuses)
	// go runTargetPortChecks(config, statuses)
	// log.Println("===== Initial monitoring run END =====")
	// --- 定时任务：每5分钟点后的第2分钟执行目录检查 ---
	go func() {
		for {
			now := time.Now()
			minute := now.Minute()
			second := now.Second()

			if minute%5 == 2 && second < 10 {
				//log.Println("[ monitorDirectories ] scheduled trigger (5min+2min) START")
				go runDirectoryChecks(config, statuses)
				//log.Println("[ monitorDirectories ] scheduled trigger (5min+2min) END")

				// 避免在同一分钟多次触发
				time.Sleep(65 * time.Second)
			}

			time.Sleep(1 * time.Second)
		}
	}()

	for {
		select {
		case <-portTicker.C:
			// 启动 goroutine 执行，防止阻塞 select 循环
			go runPortChecks(config, statuses)
		case <-processTicker.C:
			go runProcessChecks(config, statuses)
		case <-resourceTicker.C:
			go runResourceChecks(config, statuses)
		case <-pingTicker.C:
			go runPingChecks(config, statuses)
		//case <-dirTicker.C:
		//go runDirectoryChecks(config, statuses)
		case <-targetPortTicker.C:
			go runTargetPortChecks(config, statuses)
		}
	}

}

func init() {
	// 创建自定义Transport实现连接池
	transport := &http.Transport{
		MaxIdleConns:        200,                                   // 最大空闲连接
		MaxIdleConnsPerHost: 30,                                    // 每主机最大空闲连接
		IdleConnTimeout:     90 * time.Second,                      // 空闲连接超时
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // 跳过证书验证
	}

	httpClient = &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second, // 全局请求超时
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
	const tcpmaxRetries = 3
	const retryDelay = 300 * time.Millisecond
	for attempt := 1; attempt < tcpmaxRetries; attempt++ {
		tcpConn, tcpErr := net.DialTimeout("tcp", net.JoinHostPort(address, port), 3*time.Second)
		if tcpErr == nil {
			tcpConn.Close()
			return true
		}
		if attempt < tcpmaxRetries {
			//log.Printf("服务器 %s 端口 %s 通信失败，第 %d 次重试...", address, port, attempt)
			time.Sleep(retryDelay)
		}
		//return false
	}

	// 检查UDP端口
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(address, port))
	if err != nil {
		return false
	}

	// 增加重试机制
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		if checkUDP(udpAddr) {
			return true
		}
		time.Sleep(100 * time.Millisecond) // 每次重试间隔
	}

	// 如果重试次数用尽仍未成功，则认为端口关闭
	log.Printf("服务器 %s 端口 %s TCP 和 UDP 均无法通信，最终检测失败。", address, port)
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
	const maxRetries = 2
	const waitBetweenRetries = 2 * time.Second

	for retry := 0; retry <= maxRetries; retry++ {
		if retry > 0 {
			time.Sleep(waitBetweenRetries)
			fmt.Printf("%s 地址 %s 检测失败，第 %d 次重试...\n", time.Now().Format("2006-01-02 15:04:05"), address, retry)
		}

		// 执行ping命令
		cmd := exec.Command("ping", "-c", "2", "-W", "3", address)
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

// 初始化连接池 (在main函数中调用)
func initSMTPPool(emailConfig EmailConfig, poolSize int) {
	smtpClientPool = make(chan *smtp.Client, poolSize)

	for i := 0; i < poolSize; i++ {
		conn, err := createSMTPConnection(emailConfig)
		if err == nil {
			smtpClientPool <- conn
		}
	}
}

// 创建持久化连接
func createSMTPConnection(emailConfig EmailConfig) (*smtp.Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	conn, err := tls.Dial("tcp", emailConfig.SMTPHost+":"+emailConfig.SMTPPort, tlsConfig)
	if err != nil {
		return nil, err
	}

	client, err := smtp.NewClient(conn, emailConfig.SMTPHost)
	if err != nil {
		return nil, err
	}

	auth := smtp.PlainAuth("", emailConfig.From, emailConfig.Password, emailConfig.SMTPHost)
	if err := client.Auth(auth); err != nil {
		return nil, err
	}

	return client, nil
}

// 从连接池获取客户端
func getSMTPClient() (*smtp.Client, error) {
	select {
	case client := <-smtpClientPool:
		return client, nil
	default:
		return nil, fmt.Errorf("连接池耗尽")
	}
}

// 归还连接到连接池
func returnSMTPClient(client *smtp.Client) {
	smtpClientPool <- client
}

// 初始化邮件队列 (在main函数中调用)
func initEmailQueue(workers int) {
	emailQueue = &EmailQueue{
		queue: make(chan emailTask, 1000), // 缓冲队列
	}

	// 限速器：产生令牌
	tokenBucket := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(time.Duration(config.EmailRateLimit) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			select {
			case tokenBucket <- struct{}{}: // 每隔 rateLimit 秒发一个令牌
			default:
			}
		}
	}()

	// 启动多个 worker
	for i := 0; i < workers; i++ {
		emailQueue.wg.Add(1)
		go func(id int) {
			defer emailQueue.wg.Done()
			for task := range emailQueue.queue {
				<-tokenBucket // ⏳ 等待令牌（保证全局限速）
				sendEmailInternal(task.emailConfig, task.alertLevel, task.data)
			}
		}(i)
	}
}

// 添加邮件任务到队列
func (q *EmailQueue) AddTask(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	q.queue <- emailTask{emailConfig, alertLevel, data}
}

// 关闭队列 (在main退出时调用)
func (q *EmailQueue) Close() {
	close(q.queue)
	q.wg.Wait()
}

// 外部调用接口 (替换原sendEmail)
func sendEmail(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	if !config.EnableEmail {
		log.Printf("邮件发送被禁用，告警内容仅记录日志: %s", data.Subject)
		return
	}
	// 添加任务到队列
	if emailQueue != nil {
		emailQueue.AddTask(emailConfig, alertLevel, data)
	} else {
		// 降级处理：直接发送（不推荐）
		sendEmailInternal(emailConfig, alertLevel, data)
	}
}

// 发送邮件函数（两个变量：邮件配置结构体声明的对象、主题、邮件内容）
// 发送邮件函数（带健康检查和连接重建）
func sendEmailInternal(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
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

	from := emailConfig.From
	to := strings.Join(emailConfig.Recipients, ",")
	encodedSubject := mime.QEncoding.Encode("utf-8", data.Subject)

	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   encodedSubject,
		"MIME-Version":              "1.0",
		"Content-Type":              "text/html; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
	}

	var emailContent bytes.Buffer
	for k, v := range headers {
		fmt.Fprintf(&emailContent, "%s: %s\r\n", k, v)
	}
	fmt.Fprintf(&emailContent, "\r\n")
	emailContent.Write(body.Bytes())

	// 获取SMTP客户端（优先池子）
	client, err := getSMTPClient()
	if err != nil {
		// 池子空了 → 新建一个
		client, err = createSMTPConnection(emailConfig)
		if err != nil {
			log.Printf("创建SMTP连接失败: %v", err)
			return
		}
		defer client.Close()
	} else {
		// 检查连接是否健康
		if err := client.Noop(); err != nil {
			log.Printf("SMTP连接失效，重建中: %v", err)
			client.Close()
			client, err = createSMTPConnection(emailConfig)
			if err != nil {
				log.Printf("重建SMTP连接失败: %v", err)
				return
			}
			defer client.Close()
		} else {
			// 连接是健康的 → 用完还回池子
			defer returnSMTPClient(client)
		}
	}

	// 设置发件人
	if err := client.Mail(emailConfig.From); err != nil {
		log.Printf("设置发件人失败: %v，尝试重建连接", err)
		client.Close()
		client, err = createSMTPConnection(emailConfig)
		if err != nil {
			log.Printf("重建SMTP连接失败: %v", err)
			return
		}
		defer client.Close()

		if err := client.Mail(emailConfig.From); err != nil {
			log.Printf("重建连接后仍设置发件人失败: %v", err)
			return
		}
	}

	// 设置收件人
	for _, recipient := range emailConfig.Recipients {
		if err := client.Rcpt(recipient); err != nil {
			log.Printf("设置收件人失败: %v", err)
			continue
		}
	}

	// 写入数据
	wc, err := client.Data()
	if err != nil {
		log.Printf("获取邮件数据流失败: %v", err)
		return
	}
	defer wc.Close()

	if _, err := wc.Write(emailContent.Bytes()); err != nil {
		log.Printf("写入邮件数据失败: %v", err)
		return
	}

	log.Printf("邮件发送成功: %s", data.Subject)
}

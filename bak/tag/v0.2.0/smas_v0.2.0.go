package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 服务器监控指标的配置结构体
type Server struct {
	Address         string   `json:"address"`
	Port            string   `json:"port"`
	Processes       []string `json:"processes"`       // 支持多个进程
	CPUThreshold    float64  `json:"cpuThreshold"`    // CPU利用率告警阈值
	MemoryThreshold float64  `json:"memoryThreshold"` // 内存利用率告警阈值
	DiskThreshold   float64  `json:"diskThreshold"`   // 磁盘利用率告警阈值
	FolderPath      float64  `json:"folder_path"`
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
	Email   EmailConfig `json:"email"`
	Servers []Server    `json:"servers"`
}

// 服务器告警发送状态的结构体
type ServerStatus struct {
	PortAlertSent    bool
	ProcessAlertSent map[string]bool // 记录每个进程的告警状态
	CpuAlertSent     bool
	MemAlertSent     bool
	DiskAlertSent    map[string]bool
	DirAlertSent     map[string]bool
	FileAlertSent    map[string]bool
	PingAlertSent    bool
}

// StatusResponse 表示 /check 接口的响应
type StatusResponse struct {
	DirectoryStatuses []DirectoryStatus `json:"directoryStatuses"`
	ProcessStatuses   []ProcessStatus   `json:"processStatuses"`
	Metrics           Metrics           `json:"metrics"`
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

// 全局状态变量，记录每个服务器的状态
var (
	statusesMutex sync.Mutex // 全局状态锁
	templates     *template.Template
)

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

// 加载配置文件
func loadConfig(filename string) (Config, error) {
	var config Config
	file, err := os.Open(filename)
	if err != nil {
		return config, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %w", err)
	}

	err = json.Unmarshal(bytes, &config)
	if err != nil {
		return config, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return config, nil
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

// 监控服务器端口状态（端口通信状态告警）
func monitorServerPorts(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorServerPorts function")
	var wg sync.WaitGroup          // 使用 WaitGroup 来等待所有 goroutine 完成
	sem := make(chan struct{}, 10) // 限制并发数为 10
	for _, server := range config.Servers {
		wg.Add(1)         // 添加一个 goroutine 到 WaitGroup 中
		sem <- struct{}{} // 占用一个并发槽
		go func(server Server) {
			defer wg.Done()
			defer func() { <-sem }() // 释放一个并发槽
			address := server.Address
			port := server.Port
			key := fmt.Sprintf("%s:%s", address, port)
			serverState := checkPingState(address)

			// 检查服务器是否可达，如果不可达则不检测端口状态
			if !serverState {
				log.Printf("%s has lost connection, do not detect port state.", address)
				return
			}

			portState := checkPort(address, port)

			if statuses[key] == nil { // 如果该服务器的状态尚未初始化，则初始化
				statuses[key] = &ServerStatus{ // 初始化 ServerStatus 结构体
					PortAlertSent: false,
				}
			}
			// 使用锁保护对状态的访问
			statusesMutex.Lock()
			// 检测端口通信状态的邮件告警逻辑
			if port != "" {
				if !portState && !statuses[key].PortAlertSent {
					data := EmailTemplateData{
						Subject:   "⚠️⚠️端口失联告警",
						Server:    address,
						Message:   fmt.Sprintf("服务器端口 %s 通信失联", port),
						Action:    "请检查端口相关的进程是否有存在，或者是否有重启现象！",
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "severe", data)
					statuses[key].PortAlertSent = true
					fmt.Println(data.Message)
				} else if portState && statuses[key].PortAlertSent {
					//log.Printf("Port open status for %s:%s - portOpen: %v, PortAlertSent: %v", address, port, portOpen, statuses[key].PortAlertSent)
					//message := fmt.Sprintf("服务器地址:\t%s\n信息:服务器端口\t%s\t通信已恢复\t", address, port)
					//sendEmail(config.Email, "端口通信恢复", message)
					data := EmailTemplateData{
						Subject:   "✅端口通信恢复",
						Server:    address,
						Message:   fmt.Sprintf("服务器端口 %s 通信已恢复正常。", port),
						Action:    "服务器端口已恢复正常，请知悉！",
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "recovery", data)
					statuses[key].PortAlertSent = false
					fmt.Println(data.Message)
				}
			}
			statusesMutex.Unlock() // 释放锁
		}(server)
	}
	// 等待所有 goroutine 完成
	wg.Wait()
	log.Println("left monitorServerPorts function")
}

// 监控服务器通信状态（是否ping通）
func monitorServersState(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorServersState function")
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // 限制并发数为 10
	for _, server := range config.Servers {
		wg.Add(1)
		sem <- struct{}{} // 占用一个并发槽
		go func(server Server) {
			defer wg.Done()
			defer func() { <-sem }() // 释放一个并发槽

			address := server.Address
			port := server.Port
			key := fmt.Sprintf("%s:%s", address, port)

			pingState := checkPingState(address)

			// 检测服务器通信状态的邮件告警逻辑
			if !pingState && !statuses[key].PingAlertSent {
				//message := fmt.Sprintf("故障服务器地址:\t%s\n故障信息:服务器通信失联\n备注：Ping模式检测的通信状态，失联请确认是否为瞬断现象，否则请抓紧处理！", address)
				//sendEmail(config.Email, "★★★服务器通信失联告警--Connection Lost★★★", message)
				data := EmailTemplateData{
					Subject:   "🚨🚨服务器失联告警",
					Server:    address,
					Message:   "服务器通信失联，如影响业务请及时处理！",
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "critical", data)
				statuses[key].PingAlertSent = true
				fmt.Println(data.Message)
			} else if pingState && statuses[key].PingAlertSent {
				//log.Printf("Port open status for %s:%s - portOpen: %v, PortAlertSent: %v", address, port, portOpen, statuses[key].PortAlertSent)
				//message := fmt.Sprintf("服务器地址:\t%s\n信息:服务器通信已恢复\t", address)
				//sendEmail(config.Email, "★服务器通信恢复--Connection Recover★", message)
				data := EmailTemplateData{
					Subject:   "✅服务器通信恢复",
					Server:    address,
					Message:   "服务器通信已恢复正常，请检查所承载业务是否已正常启动！",
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "recovery", data)
				statuses[key].PingAlertSent = false
				fmt.Println(data.Message)
			}
		}(server)
	}
	wg.Wait()
	log.Println("left monitorServersState function")
}

// 监控服务器进程状态
func monitorServersProcess(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorServersProcess function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // 限制并发数为10
	for _, server := range config.Servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(server Server) {
			defer wg.Done()
			defer func() { <-sem }()
			address := server.Address
			port := server.Port
			key := fmt.Sprintf("%s:%s", address, port)

			serverState := checkPingState(address)
			if !serverState {
				log.Printf("%s has lost connection, do not detect process status...", address)
				return
			}

			url := fmt.Sprintf("http://%s:9600/check", address) // 配置访问客户端进程状态的链接
			resp, err := http.Get(url)
			if err != nil {
				log.Printf("Failed to check metrics_exporter on server %s: %v", address, err)
				return
			}

			defer resp.Body.Close()
			// 读取响应并存储到 `bodyData` 变量中
			bodyData, _ := io.ReadAll(resp.Body)
			//log.Printf("Response from server %s: %s", address, string(bodyData))

			// 重置 `resp.Body`，使其可以被重新读取；因为body第一次被读取之后流就会被消费，无法再次读取
			resp.Body = io.NopCloser(bytes.NewBuffer(bodyData))

			var statusResponse StatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
				return // 继续检查下一个服务器
			}
			// 使用锁保护对状态的访问
			statusesMutex.Lock()

			for _, result := range statusResponse.ProcessStatuses {
				process_name := result.ProcessName // 进程名称
				is_running := result.IsRunning     // 进程是否运行
				// 初始化每个进程的告警状态
				// ProcessAlertSent 是一个 map，用于记录每个进程的告警状态
				// statuses[key] 是一个 map，定义在本函数的参数中，key 是服务器地址和端口的组合
				if _, exists := statuses[key].ProcessAlertSent[process_name]; !exists {
					statuses[key].ProcessAlertSent[process_name] = false
				}
				// 检测进程状态的邮件告警逻辑
				if !is_running && !statuses[key].ProcessAlertSent[process_name] {
					log.Printf("Checking server %s: ProcessName=%v, IsRunning=%v", address, process_name, is_running)
					//message := fmt.Sprintf("服务器 %s 的进程 %s 没有运行，请检查！", address, process_name)
					data := EmailTemplateData{
						Subject:   "⚠️进程消失告警",
						Server:    address,
						Message:   fmt.Sprintf("服务器进程 %s 不存在，请检查！", process_name),
						Action:    "请登录服务器检查进程是否存在，或者是否有重启现象！",
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "severe", data)
					//sendEmail(config.Email, "进程消失告警", message)
					statuses[key].ProcessAlertSent[process_name] = true
					fmt.Println(data.Message)

				} else if is_running && statuses[key].ProcessAlertSent[process_name] {
					log.Printf("Checking server %s: ProcessName=%v, IsRunning=%v", address, process_name, is_running)
					//message := fmt.Sprintf("服务器 %s 的进程 %s 已恢复运行！", address, process_name)
					//log.Printf("Sending process missing alert: %s", message)
					//sendEmail(config.Email, "进程恢复告警", message)
					data := EmailTemplateData{
						Subject:   "✅进程已启动",
						Server:    address,
						Message:   fmt.Sprintf("服务器进程 %s 已启动！", process_name),
						Action:    "进程已启动，请观察是否有异常！",
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "recovery", data)
					statuses[key].ProcessAlertSent[process_name] = false
					fmt.Println(data.Message)
				}

			}
			statusesMutex.Unlock() // 释放锁
		}(server)

	}
	wg.Wait()
	log.Println("left monitorServersProcess function") // 调试日志
}

// 监控服务器的状态，包括CPU、内存、磁盘利用率
func monitorResources(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorResource function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // 限制并发数为10
	for _, server := range config.Servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(server Server) {
			defer wg.Done()
			defer func() { <-sem }() // 释放一个并发槽
			address := server.Address
			port := server.Port
			// 获取服务器的阈值配置
			cputhre := server.CPUThreshold
			memthre := server.MemoryThreshold
			diskthre := server.DiskThreshold

			serverState := checkPingState(address)
			if !serverState {
				log.Printf("%s has lost connection, do not detect Server resource status...", address)
				return
			}

			key := fmt.Sprintf("%s:%s", address, port)
			// 拼接访问服务器资源使用情况的URL
			url := fmt.Sprintf("http://%s:9600/check", address)

			resp, err := http.Get(url)
			if err != nil {
				log.Printf("Failed to fetch metrics from server %s: %v", address, err)
				return
			}
			defer resp.Body.Close()

			var statusResponse StatusResponse
			// 解析响应体中的 JSON 数据
			if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
				log.Printf("Failed to decode metrics from server %s: %v", address, err)
				return
			}
			metrics := statusResponse.Metrics

			// 使用锁保护对状态的访问
			statusesMutex.Lock()
			// 判断是否超出阈值并发送告警
			if metrics.CPUUsage > cputhre && !statuses[key].CpuAlertSent {
				//message := fmt.Sprintf("告警: 服务器 %s 的CPU使用率过高: %.2f%%", address, metrics.CPUUsage)
				//sendEmail(config.Email, "CPU使用率告警", message)
				data := EmailTemplateData{
					Subject:   "⚠️CPU使用率告警",
					Server:    address,
					Message:   "CPU使用率超过阈值",
					Value:     fmt.Sprintf("%.2f%%", metrics.CPUUsage),
					Threshold: fmt.Sprintf("%.2f%%", server.CPUThreshold),
					Action:    "请检查服务器负载情况，必要时进行扩容或优化！",
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "warning", data)
				statuses[key].CpuAlertSent = true
			} else if metrics.CPUUsage < cputhre && statuses[key].CpuAlertSent {
				//message := fmt.Sprintf("信息: 服务器 %s 的CPU使用率已整合降低: %.2f%%", address, metrics.CPUUsage)
				//sendEmail(config.Email, "CPU使用率恢复告警", message)
				data := EmailTemplateData{
					Subject:   "✅CPU使用率已降低",
					Server:    address,
					Message:   "CPU使用率已降低，恢复正常",
					Value:     fmt.Sprintf("%.2f%%", metrics.CPUUsage),
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "recovery", data)
				statuses[key].CpuAlertSent = false
			}
			if metrics.MemoryUsage > memthre && !statuses[key].MemAlertSent {
				//message := fmt.Sprintf("告警: 服务器 %s 的内存使用率过高: %.2f%%", address, metrics.MemoryUsage)
				//sendEmail(config.Email, "内存使用率告警", message)
				data := EmailTemplateData{
					Subject:   "⚠️内存使用率告警",
					Server:    address,
					Message:   "内存使用率超过阈值！",
					Value:     fmt.Sprintf("%.2f%%", metrics.MemoryUsage),
					Threshold: fmt.Sprintf("%.2f%%", server.MemoryThreshold),
					Action:    "请检查服务器进程占用内存情况，必要时进程内存分配优化或扩容！",
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "warning", data)
				statuses[key].MemAlertSent = true
			} else if metrics.MemoryUsage < memthre && statuses[key].MemAlertSent {
				//message := fmt.Sprintf("信息: 服务器 %s 的内存使用率已降低: %.2f%%", address, metrics.MemoryUsage)
				//sendEmail(config.Email, "内存使用率恢复告警", message)
				data := EmailTemplateData{
					Subject:   "✅内存使用率已降低",
					Server:    address,
					Message:   "服务器内存使用率已降低，恢复正常！",
					Value:     fmt.Sprintf("%.2f%%", metrics.MemoryUsage),
					Timestamp: time.Now().Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "recovery", data)
				statuses[key].MemAlertSent = false
			}
			// 按照磁盘挂载点分别检查使用率
			for mountpoint, usage := range metrics.DiskUsage {
				if usage > diskthre && !statuses[key].DiskAlertSent[mountpoint] {
					//message := fmt.Sprintf("告警信息: 服务器 %s 的磁盘使用率过高 \n挂载点: %s: %.2f%%", address, mountpoint, usage)
					//sendEmail(config.Email, "磁盘使用率告警", message)
					data := EmailTemplateData{
						Subject:   "⚠️磁盘使用率告警",
						Server:    address,
						Message:   "磁盘利用率超过阈值！",
						Value:     fmt.Sprintf("%.2f%% 挂载点：%s", metrics.DiskUsage[mountpoint], mountpoint),
						Threshold: fmt.Sprintf("%.2f%%", server.DiskThreshold),
						Action:    "请检查服务器磁盘分区使用情况，必要时进行清理或扩容！",
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "warning", data)
					statuses[key].DiskAlertSent[mountpoint] = true
				} else if usage < diskthre && statuses[key].DiskAlertSent[mountpoint] {
					//message := fmt.Sprintf("信息: 服务器 %s 的磁盘使用率已降低 \n挂载点: %s: %.2f%%", address, mountpoint, usage)
					//sendEmail(config.Email, "磁盘使用率恢复告警", message)
					data := EmailTemplateData{
						Subject:   "✅磁盘利用率已降低",
						Server:    address,
						Message:   "服务器磁盘利用率已降低，恢复正常！",
						Value:     fmt.Sprintf("%.2f%% 挂载点：%s", metrics.DiskUsage[mountpoint], mountpoint),
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "recovery", data)
					statuses[key].DiskAlertSent[mountpoint] = false
				}

			}
			statusesMutex.Unlock() // 释放锁
		}(server)

	}
	wg.Wait()                                    // 等待所有 goroutine 完成
	log.Println("left monitorResource function") // 调试日志
}

// 监测服务器目录和文件模块
func monitorDirectory(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorDirectory function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // 限制并发数为10
	for _, server := range config.Servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(server Server) {
			defer wg.Done()
			defer func() { <-sem }() // 释放一个并发槽
			address := server.Address
			port := server.Port
			key := fmt.Sprintf("%s:%s", address, port)

			serverState := checkPingState(address)
			if !serverState {
				log.Printf("%s has lost connection, do not detect Server directory file status...", address)
				return
			}
			// 拼接访问服务器目录状态的URL
			url := fmt.Sprintf("http://%s:9600/check", address)
			resp, err := http.Get(url)
			if err != nil {
				log.Printf("Failed to check directory on server %s: %v", address, err)
				return
			}

			defer resp.Body.Close()
			// 读取响应并存储到 `bodyData` 变量中
			bodyData, _ := io.ReadAll(resp.Body)
			//log.Printf("Response from server %s: %s", address, string(bodyData))

			// 重置 `resp.Body`，使其可以被重新读取
			resp.Body = io.NopCloser(bytes.NewBuffer(bodyData))

			var statusResponse StatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
				log.Printf("Failed to decode metrics-exporter check response from server %s: %v", address, err)
				return // 继续检查下一个服务器
			}
			// 使用锁保护对状态的访问
			statusesMutex.Lock()
			for _, result := range statusResponse.DirectoryStatuses {
				baseDir := result.BaseDir
				folderExists := result.DirectoryExist
				fileExists := result.XdrFileExist

				// 1. 先判断日期目录不存在的情况，根目录无日期目录且无满足条件的文件，则发送目录告警
				if !folderExists && !fileExists {
					if !statuses[key].DirAlertSent[baseDir] {
						log.Printf("Checking server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
						//message := fmt.Sprintf("服务器 %s 的 %s 下不存在指定目录和文件，请检查！", address, baseDir)
						data := EmailTemplateData{
							Subject:   "⚠️⚠️目录/文件缺失告警",
							Server:    address,
							Message:   fmt.Sprintf("服务器目录 %s 不存在指定目录或文件！", baseDir),
							Action:    "请检查服务器输出文件进程是否正常运行！",
							Timestamp: time.Now().Format("2006-01-02 15:04:05"),
						}
						sendEmail(config.Email, "warning", data)
						//sendEmail(config.Email, "目录/文件缺失告警", message)
						statuses[key].DirAlertSent[baseDir] = true
						fmt.Println(data.Message)
					}
				} else if (folderExists || fileExists) && statuses[key].DirAlertSent[baseDir] {
					// 仅在目录或文件存在的情况下，且先前发送了目录缺失告警时，才发送恢复通知
					log.Printf("Directory or file recovered on server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
					//message := fmt.Sprintf("服务器 %s 的 %s 下的目录或文件已恢复。", address, baseDir)
					//log.Printf("Sending directory recovery notification: %s", message)
					//sendEmail(config.Email, "目录/文件恢复通知", message)
					data := EmailTemplateData{
						Subject:   "✅目录/文件已恢复",
						Server:    address,
						Message:   fmt.Sprintf("服务器目录 %s 文件/目录已恢复！", baseDir),
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
					}
					sendEmail(config.Email, "recovery", data)
					statuses[key].DirAlertSent[baseDir] = false
					fmt.Println(data.Message)
				}

				// 2. 日期目录存在，检测日期目录下文件
				if folderExists {
					if !fileExists && !statuses[key].FileAlertSent[baseDir] {
						// 日期目录存在但无指定文件，发送文件缺失告警
						log.Printf("Checking server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
						//message := fmt.Sprintf("服务器 %s 的 %s 日期目录下不存在指定文件，请检查！", address, baseDir)
						//log.Printf("Sending file missing alert: %s", message)
						//sendEmail(config.Email, "文件缺失告警", message)
						data := EmailTemplateData{
							Subject:   "⚠️⚠️文件缺失告警",
							Server:    address,
							Message:   fmt.Sprintf("服务器目录 %s 不存在指定文件！", baseDir),
							Action:    "请检查服务器输出或转移文件进程是否正常运行！",
							Timestamp: time.Now().Format("2006-01-02 15:04:05"),
						}
						sendEmail(config.Email, "warning", data)
						statuses[key].FileAlertSent[baseDir] = true
						fmt.Println(data.Message)
					} else if fileExists && statuses[key].FileAlertSent[baseDir] {
						// 文件恢复通知
						//message := fmt.Sprintf("服务器 %s 的 %s 日期目录下的文件已恢复。", address, baseDir)
						//log.Printf("Sending file recovery notification: %s", message)
						//sendEmail(config.Email, "文件恢复通知", message)
						data := EmailTemplateData{
							Subject:   "✅目录文件已恢复",
							Server:    address,
							Message:   fmt.Sprintf("服务器目录 %s 内文件已恢复！", baseDir),
							Timestamp: time.Now().Format("2006-01-02 15:04:05"),
						}
						sendEmail(config.Email, "recovery", data)
						statuses[key].FileAlertSent[baseDir] = false
					}
				}
			}
			statusesMutex.Unlock() // 释放锁
		}(server)

	}
	wg.Wait()                                     // 等待所有 goroutine 完成
	log.Println("left monitorDirectory function") // 调试日志
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

	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   data.Subject,
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
	configPath := filepath.Join(projectRoot, "conf", "config.json")
	config, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	// 初始化全局状态变量
	statuses := make(map[string]*ServerStatus)
	for _, server := range config.Servers {
		address := server.Address
		port := server.Port
		key := fmt.Sprintf("%s:%s", address, port)

		// 为每个服务器的告警状态初始化 ServerStatus 结构体
		statuses[key] = &ServerStatus{
			PortAlertSent:    false,
			ProcessAlertSent: make(map[string]bool), // 初始化为非空 map
			CpuAlertSent:     false,
			MemAlertSent:     false,
			DiskAlertSent:    make(map[string]bool), // 初始化为非空 map
			DirAlertSent:     make(map[string]bool),
			FileAlertSent:    make(map[string]bool),
			PingAlertSent:    false,
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

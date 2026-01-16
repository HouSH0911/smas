package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
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

// 获取项目根目录（bin 目录的上一级）
func getProjectRoot() (string, error) {
	exePath, err := os.Executable() // 获取当前可执行文件路径
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(exePath)), nil // 返回 bin 的上一级目录
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
	// 使用 ping 命令检查服务器是否可达，时延为8秒
	cmd := exec.Command("ping", "-c", "2", "-W", "8", address)
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Address:%s Ping detected failed: %s\n", address, err)
		return false
	}
	return true

}

// 全局状态变量，记录每个服务器的状态
var statusesMutex sync.Mutex // 全局状态锁

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
				fmt.Println("Server has lost connection, do not detect port state.")
				return
			}

			portState := checkPort(address, port)

			// 使用锁保护对状态的访问
			statusesMutex.Lock()

			if statuses[key] == nil { // 如果该服务器的状态尚未初始化，则初始化
				statuses[key] = &ServerStatus{ // 初始化 ServerStatus 结构体
					PortAlertSent: false,
				}
			}

			// 检测端口通信状态的邮件告警逻辑
			if port != "" {
				if !portState && !statuses[key].PortAlertSent {
					message := fmt.Sprintf("故障服务器地址:\t%s\n故障信息:服务器端口\t%s\t通信失联\n备注：！！！请检查端口相关的进程是否有重启现象！！！", address, port)
					sendEmail(config.Email, "端口失联告警", message)
					statuses[key].PortAlertSent = true
					fmt.Println(message)
				} else if portState && statuses[key].PortAlertSent {
					//log.Printf("Port open status for %s:%s - portOpen: %v, PortAlertSent: %v", address, port, portOpen, statuses[key].PortAlertSent)
					message := fmt.Sprintf("服务器地址:\t%s\n信息:服务器端口\t%s\t通信已恢复\t", address, port)
					sendEmail(config.Email, "端口通信恢复", message)
					statuses[key].PortAlertSent = false
					fmt.Println(message)
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
			// if statuses[address] == nil {
			// 	statuses[address] = &ServerStatus{
			// 		PingAlertSent: false,
			// 	}
			// }
			pingState := checkPingState(address)

			// 检测服务器通信状态的邮件告警逻辑
			if !pingState && !statuses[key].PingAlertSent {
				message := fmt.Sprintf("故障服务器地址:\t%s\n故障信息:服务器通信失联\n备注：Ping模式检测的通信状态，失联请确认是否为瞬断现象，否则请抓紧处理！", address)
				sendEmail(config.Email, "★★★服务器通信失联告警--Connection Lost★★★", message)
				statuses[key].PingAlertSent = true
				fmt.Println(message)
			} else if pingState && statuses[key].PingAlertSent {
				//log.Printf("Port open status for %s:%s - portOpen: %v, PortAlertSent: %v", address, port, portOpen, statuses[key].PortAlertSent)
				message := fmt.Sprintf("服务器地址:\t%s\n信息:服务器通信已恢复\t", address)
				sendEmail(config.Email, "★服务器通信恢复--Connection Recover★", message)
				statuses[key].PingAlertSent = false
				fmt.Println(message)
			}
		}(server)
	}
	wg.Wait()
	log.Println("left monitorServerState function")
}

// 监控服务器进程状态
func monitorServersProcess(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorServersProcess function") // 调试日志
	for _, server := range config.Servers {
		address := server.Address
		port := server.Port
		key := fmt.Sprintf("%s:%s", address, port)

		// serverState := checkPingState(address)
		// if !serverState {
		// 	fmt.Println("Server has lost connection, do not detect process.")
		// 	continue
		// }

		url := fmt.Sprintf("http://%s:8163/check", address) // 配置访问客户端进程状态的链接
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Failed to check directory_monitor on server %s: %v", address, err)
			continue
		}

		defer resp.Body.Close()
		// 读取响应并存储到 `bodyData` 变量中
		bodyData, _ := io.ReadAll(resp.Body)
		//log.Printf("Response from server %s: %s", address, string(bodyData))

		// 重置 `resp.Body`，使其可以被重新读取；因为body第一次被读取之后流就会被消费，无法再次读取
		resp.Body = io.NopCloser(bytes.NewBuffer(bodyData))

		var statusResponse StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
			continue // 继续检查下一个服务器
		}

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
				message := fmt.Sprintf("服务器 %s 的进程 %s 没有运行，请检查！", address, process_name)
				log.Printf("Sending process missing alert: %s", message)
				sendEmail(config.Email, "进程消失告警", message)
				statuses[key].ProcessAlertSent[process_name] = true

			} else if is_running && statuses[key].ProcessAlertSent[process_name] {
				log.Printf("Checking server %s: ProcessName=%v, IsRunning=%v", address, process_name, is_running)
				message := fmt.Sprintf("服务器 %s 的进程 %s 已恢复运行！", address, process_name)
				log.Printf("Sending process missing alert: %s", message)
				sendEmail(config.Email, "进程恢复告警", message)
				statuses[key].ProcessAlertSent[process_name] = false
			}

		}

	}
	log.Println("left monitorServersProcess function") // 调试日志
}

// 监控服务器的状态，包括CPU、内存、磁盘利用率
func monitorResources(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorResources function") // 调试日志

	for _, server := range config.Servers {
		address := server.Address
		port := server.Port
		// 获取服务器的阈值配置
		cputhre := server.CPUThreshold
		memthre := server.MemoryThreshold
		diskthre := server.DiskThreshold
		// serverState := checkPingState(address)
		// if !serverState {
		// 	fmt.Println("Server has lost connection, do not detect resources.")
		// 	continue
		// }
		key := fmt.Sprintf("%s:%s", address, port)
		// 拼接访问服务器资源使用情况的URL
		url := fmt.Sprintf("http://%s:8761/metrics", address)

		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Failed to fetch metrics from server %s: %v", address, err)
			continue
		}
		defer resp.Body.Close()

		var metrics Metrics
		// 解析响应体中的 JSON 数据
		if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
			log.Printf("Failed to decode metrics from server %s: %v", address, err)
			continue
		}

		// 判断是否超出阈值并发送告警
		if metrics.CPUUsage > cputhre && !statuses[key].CpuAlertSent {
			message := fmt.Sprintf("告警: 服务器 %s 的CPU使用率过高: %.2f%%", address, metrics.CPUUsage)
			sendEmail(config.Email, "CPU使用率告警", message)
			statuses[key].CpuAlertSent = true
		} else if metrics.CPUUsage < cputhre && statuses[key].CpuAlertSent {
			message := fmt.Sprintf("信息: 服务器 %s 的CPU使用率已整合降低: %.2f%%", address, metrics.CPUUsage)
			sendEmail(config.Email, "CPU使用率恢复告警", message)
			statuses[key].CpuAlertSent = false
		}
		if metrics.MemoryUsage > memthre && !statuses[key].MemAlertSent {
			message := fmt.Sprintf("告警: 服务器 %s 的内存使用率过高: %.2f%%", address, metrics.MemoryUsage)
			sendEmail(config.Email, "内存使用率告警", message)
			statuses[key].MemAlertSent = true
		} else if metrics.MemoryUsage < memthre && statuses[key].MemAlertSent {
			message := fmt.Sprintf("信息: 服务器 %s 的内存使用率已降低: %.2f%%", address, metrics.MemoryUsage)
			sendEmail(config.Email, "内存使用率恢复告警", message)
			statuses[key].MemAlertSent = false
		}
		// 按照磁盘挂载点分别检查使用率
		for mountpoint, usage := range metrics.DiskUsage {
			if usage > diskthre && !statuses[key].DiskAlertSent[mountpoint] {
				message := fmt.Sprintf("告警信息: 服务器 %s 的磁盘使用率过高 \n挂载点: %s: %.2f%%", address, mountpoint, usage)
				sendEmail(config.Email, "磁盘使用率告警", message)
				statuses[key].DiskAlertSent[mountpoint] = true
			} else if usage < diskthre && statuses[key].DiskAlertSent[mountpoint] {
				message := fmt.Sprintf("信息: 服务器 %s 的磁盘使用率已降低 \n挂载点: %s: %.2f%%", address, mountpoint, usage)
				sendEmail(config.Email, "磁盘使用率恢复告警", message)
				statuses[key].DiskAlertSent[mountpoint] = false
			}
		}
	}
	log.Println("left monitorResource function") // 调试日志
}

// 监测服务器目录和文件模块
func monitorDirectory(config Config, statuses map[string]*ServerStatus) {
	log.Println("Entering monitorDirectory function") // 调试日志
	for _, server := range config.Servers {
		address := server.Address
		port := server.Port
		key := fmt.Sprintf("%s:%s", address, port)
		// 拼接访问服务器目录状态的URL
		url := fmt.Sprintf("http://%s:8163/check", address)
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Failed to check directory on server %s: %v", address, err)
			continue
		}

		defer resp.Body.Close()
		// 读取响应并存储到 `bodyData` 变量中
		bodyData, _ := io.ReadAll(resp.Body)
		//log.Printf("Response from server %s: %s", address, string(bodyData))

		// 重置 `resp.Body`，使其可以被重新读取
		resp.Body = io.NopCloser(bytes.NewBuffer(bodyData))

		var statusResponse StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
			log.Printf("Failed to decode directory_monitor check response from server %s: %v", address, err)
			continue // 继续检查下一个服务器
		}

		for _, result := range statusResponse.DirectoryStatuses {
			baseDir := result.BaseDir
			folderExists := result.DirectoryExist
			fileExists := result.XdrFileExist

			// 1. 先判断日期目录不存在的情况，根目录无日期目录且无满足条件的文件，则发送目录告警
			if !folderExists && !fileExists {
				if !statuses[key].DirAlertSent[baseDir] {
					log.Printf("Checking server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
					message := fmt.Sprintf("服务器 %s 的 %s 下不存在指定目录和文件，请检查！", address, baseDir)
					log.Printf("Sending directory missing alert: %s", message)
					sendEmail(config.Email, "目录/文件缺失告警", message)
					statuses[key].DirAlertSent[baseDir] = true
				}
			} else if (folderExists || fileExists) && statuses[key].DirAlertSent[baseDir] {
				// 仅在目录或文件存在的情况下，且先前发送了目录缺失告警时，才发送恢复通知
				log.Printf("Directory or file recovered on server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
				message := fmt.Sprintf("服务器 %s 的 %s 下的目录或文件已恢复。", address, baseDir)
				log.Printf("Sending directory recovery notification: %s", message)
				sendEmail(config.Email, "目录/文件恢复通知", message)
				statuses[key].DirAlertSent[baseDir] = false
			}

			// 2. 日期目录存在，检测日期目录下文件
			if folderExists {
				if !fileExists && !statuses[key].FileAlertSent[baseDir] {
					// 日期目录存在但无指定文件，发送文件缺失告警
					log.Printf("Checking server %s: DirectoryExist=%v, XdrFileExist=%v", address, folderExists, fileExists)
					message := fmt.Sprintf("服务器 %s 的 %s 日期目录下不存在指定文件，请检查！", address, baseDir)
					log.Printf("Sending file missing alert: %s", message)
					sendEmail(config.Email, "文件缺失告警", message)
					statuses[key].FileAlertSent[baseDir] = true
				} else if fileExists && statuses[key].FileAlertSent[baseDir] {
					// 文件恢复通知
					message := fmt.Sprintf("服务器 %s 的 %s 日期目录下的文件已恢复。", address, baseDir)
					log.Printf("Sending file recovery notification: %s", message)
					sendEmail(config.Email, "文件恢复通知", message)
					statuses[key].FileAlertSent[baseDir] = false
				}
			}
		}

	}
	log.Println("left monitorDirectory function") // 调试日志
}

// 发送邮件函数（两个变量：邮件配置结构体声明的对象、主题、邮件内容）
func sendEmail(emailConfig EmailConfig, subject, body string) {
	// 获取配置文件的邮件配置相关信息
	from := emailConfig.From
	to := strings.Join(emailConfig.Recipients, ",")
	smtpHost := emailConfig.SMTPHost
	smtpPort := emailConfig.SMTPPort

	// 构建邮件内容
	var bodyBuffer bytes.Buffer // 用于存储和操作字节数据
	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   subject,
		"MIME-Version":              "1.0",
		"Content-Type":              "text/plain; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
	}

	for k, v := range headers {
		fmt.Fprintf(&bodyBuffer, "%s: %s\r\n", k, v) // 将headers写入bodyBuffer
	}
	fmt.Fprintf(&bodyBuffer, "\r\n")

	quotedBody := quotedprintable.NewWriter(&bodyBuffer) // 将bodyBuffer包装为quoted-printable编码的Writer
	quotedBody.Write([]byte(body))                       // 将邮件内容写入quotedBody
	quotedBody.Close()

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
	_, err = wc.Write(bodyBuffer.Bytes())
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
				time.Sleep(5 * time.Second)
			}
		}()
		// 启动监控服务器通信状态
		go func() {
			for {
				monitorServersState(config, statuses)
				// 每 10 秒检查一次
				time.Sleep(10 * time.Second)
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
				// 每 90 秒检查一次
				time.Sleep(120 * time.Second)
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
				time.Sleep(20 * time.Second)
			}
		}()

		// 阻塞主线程，保持程序运行
		select {}
	}
}

package main

import (
	"crypto/tls"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	Datacenter      string   `json:"datacenter"`      // 机房标识
	//FolderPath         float64  `json:"folder_path"`
	ExcludeMountPoints []string `json:"excludeMountPoints"` // 排除的磁盘挂载点

	// *** v2.4.2新增：分组级别的排除开关 (使用指针以区分"未配置"和"false") ***
	// 默认不配置(nil) = 开启检测；配置为 false = 关闭检测
	ProcessCheck    *bool  `json:"processCheck"`    // 进程检测开关
	PortCheck       *bool  `json:"portCheck"`       // 端口检测开关 (本机)
	PingCheck       *bool  `json:"pingCheck"`       // Ping检测开关
	ResourceCheck   *bool  `json:"resourceCheck"`   // 资源(CPU/内存/磁盘)检测开关
	DirectoryCheck  *bool  `json:"directoryCheck"`  // 目录/文件检测开关
	TargetPortCheck *bool  `json:"targetPortCheck"` // 目标端口(raport)检测开关
	ServerType      string `json:"serverType"`      // 服务器类型 (如 SMAS/SJJS)
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
	Email        EmailConfig        `json:"email"`
	WechatWork   WechatWorkConfig   `json:"wechatWork"`
	AlertMethods AlertMethodsConfig `json:"alertMethods"`
	// 新增字段
	SummaryReport struct {
		Enabled    bool   `json:"enabled"`
		ReportTime string `json:"reportTime"` // 例如 "08:00"
		ReportType string `json:"reportType"` // "daily" or "weekly"
		Title      string `json:"title"`
	} `json:"summaryReport"`
	Servers                []Server      `json:"servers"`
	Monitor                MonitorConfig `json:"monitor"`                // 全局监控配置
	EmailRateLimit         int           `json:"emailRateLimit"`         // 新增邮件发送频率限制字段
	EnableEmail            bool          `json:"enableEmail"`            // 是否启用邮件发送
	ResourceSmooth         float64       `json:"resourceEma"`            // 资源监控平滑指数
	ConsecutiveToAlert     int           `json:"consecutiveToAlert"`     // 连续多少次超过阈值后告警
	ConsecutiveToRecover   int           `json:"consecutiveToRecover"`   // 连续多少次低于阈值后恢复告警
	HttpTimeout            int           `json:"httpTimeout"`            // 请求缓存数据的HTTP请求超时时间
	FailureCooldown        int           `json:"failureCooldown"`        // 失败冷却时间（秒）
	PingCoolDown           int           `json:"pingCoolDown"`           // Ping失败冷却时间（秒）
	PortCoolDown           int           `json:"portCoolDown"`           // 端口失败冷却时间（秒）
	PortTimeout            int           `json:"portTimeout"`            // 22端口检测超时时间（秒）
	TcpAndUdpResultAddTime int           `json:"tcpAndUdpResultAddTime"` // TCP和UDP结果累加时间（毫秒）
	TcpPortDetectTimeout   int           `json:"tcpPortDetectTimeout"`   // TCP端口检测超时时间（毫秒）
	UdpPortDetectTimeout   int           `json:"udpPortDetectTimeout"`   // UDP端口检测超时时间（毫秒）
	// *** v2.2.0新增以下两行 ***
	ProcessRestartWindow int `json:"processRestartWindow"` // 进程重启判断窗口(秒)
	ServerRestartWindow  int `json:"serverRestartWindow"`  // 服务器重启判断窗口(秒)
	PortRestartWindow    int `json:"portRestartWindow"`    // 端口通信重启判断窗口(秒)
	// *** v2.5.0新增字段 ***
	PortProcessMappings []PortProcessMapping `json:"portProcessMappings"`
	// *** [v2.5.0新增] Ping检测配置 ***
	EnableIcmpPing bool `json:"enableIcmpPing"` // 是否启用 ICMP Ping
	EnableTcpPing  bool `json:"enableTcpPing"`  // 是否启用 TCP(22端口) Ping
	IcmpTimeout    int  `json:"icmpTimeout"`    // ICMP 超时时间(秒)
	SshPort        int  `json:"sshPort"`        // 用于 TCP Ping 的端口号
	// *** [v2.5.0新增] 话单流统计报告配置 ***
	StreamReport struct {
		Enabled       bool     `json:"enabled"`
		ReportTime    string   `json:"reportTime"` // 例如 "09:00"
		TargetStreams []string `json:"targetStreams"`
	} `json:"streamReport"`
}

// 企业微信消息结构
type WechatWorkMessage struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content             string   `json:"content"`
		MentionedList       []string `json:"mentioned_list,omitempty"`
		MentionedMobileList []string `json:"mentioned_mobile_list,omitempty"`
	} `json:"text"`
	Markdown struct {
		Content string `json:"content"`
	} `json:"markdown"`
	// 新增下面这个字段
	File struct {
		MediaId string `json:"media_id"`
	} `json:"file,omitempty"`
}

// 新增结构体：用于解析上传响应
type WechatMediaResponse struct {
	ErrCode   int    `json:"errcode"`
	ErrMsg    string `json:"errmsg"`
	Type      string `json:"type"`
	MediaId   string `json:"media_id"`
	CreatedAt string `json:"created_at"`
}

// AlertRecord 告警记录结构体
type AlertRecord struct {
	Time       string `json:"time"`
	Server     string `json:"server"`
	AlertLevel string `json:"alert_level"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Value      string `json:"value,omitempty"`
	Threshold  string `json:"threshold,omitempty"`
	Action     string `json:"action,omitempty"`
	Status     string `json:"status,omitempty"`
	Port       string `json:"port,omitempty"`
}

// v2.5.0新增端口与进程映射结构体
type PortProcessMapping struct {
	Name      string   `json:"name"`      // 映射组名称，方便日志打印
	Ports     []string `json:"ports"`     // 该组包含的端口
	Processes []string `json:"processes"` // 该组包含的进程
}

// 定义单条流统计数据的结构
type StreamStat struct {
	StreamName string `json:"streamName"`
	TotalFiles int64  `json:"totalFiles"`
	TotalSize  int64  `json:"totalSize"`
	StatDate   string `json:"statDate"`
}

// 服务器告警发送状态的结构体
type ServerStatus struct {
	//PortAlertSent int32
	//ProcessAlertSent map[string]int32 // 记录每个进程的告警状态
	PortStates map[string]*StateTracker
	PortMutex  sync.Mutex // 新增锁

	ProcessStates map[string]*StateTracker // [v2.2.0新增] 改为存储状态对象
	ProcessMutex  sync.Mutex               // 添加互斥锁字段

	CpuAlertSent int32
	MemAlertSent int32

	DiskAlertSent map[string]int32
	DiskMutex     sync.Mutex // 添加互斥锁字段

	DirAlertSent  map[string]int32
	FileAlertSent map[string]int32
	DirMutex      sync.Mutex // 添加互斥锁字段

	PingState *StateTracker // [v2.2.0新增] 用于Ping的状态追踪

	//TargetPortMutex     sync.Mutex       // 添加互斥锁字段
	//TargetPortAlertSent map[string]int32 // 记录每个目标服务器端口的告警状态
	TargetPortStates map[string]*StateTracker
	TargetPortMutex  sync.Mutex
	// 去抖与平滑字段（新增）
	ResourceMutex  sync.Mutex // 资源告警相关互斥
	CpuHighCount   int        // 连续高于阈值计数
	CpuNormalCount int        // 连续低于阈值计数（用于恢复）
	LastCPUEMA     float64    // 上一次的指数平滑值（初始 0 表示未初始化）

}

// *** v2.2.0新增结构体：用于记录状态变更历史 ***
type StateTracker struct {
	FirstFailureTime time.Time // 首次检测到异常的时间 (IsZero代表正常)
	AlertSent        bool      // 是否已经发送了"Down/Lost"确认告警
	// [v2.5.0新增字段] 记录最后一次进程恢复的时间
	LastProcessRecoveryTime time.Time
}

// StatusResponse 表示 /check 接口的响应
type StatusResponse struct {
	DirectoryStatuses []DirectoryStatus `json:"directoryStatuses"`
	ProcessStatuses   []ProcessStatus   `json:"processStatuses"`
	Metrics           Metrics           `json:"metrics"`
	PortStatuses      []PortStatus      `json:"portStatuses"`
	StreamStats       []StreamStat      `json:"streamStats"`
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
	Datacenter string // 机房标识
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

// 用于记录上次失败时间和是否已提示
type FailureRecord struct {
	LastFail time.Time
	Notified bool
}

// 企业微信配置
type WechatWorkConfig struct {
	Enabled             bool     `json:"enabled"`
	WebhookUrl          string   `json:"webhookUrl"`
	ProxyEnabled        bool     `json:"proxyEnabled"` // 新增：是否启用代理
	ProxyUrl            string   `json:"proxyUrl"`     // 新增：代理服务器地址
	MentionedList       []string `json:"mentionedList"`
	MentionedMobileList []string `json:"mentionedMobileList"`
}

// 告警方式配置
type AlertMethodsConfig struct {
	Email      bool `json:"email"`
	WechatWork bool `json:"wechatWork"`
}

// 每周/每日发送告警汇总的统计结构体
type SummaryStats struct {
	TotalAlerts  int               `json:"total_alerts"`
	IPStats      map[string]IPStat `json:"ip_stats"`      // 按IP统计
	LevelStats   map[string]int    `json:"level_stats"`   // 按级别统计
	TypeStats    map[string]int    `json:"type_stats"`    // 按类型统计
	MessageStats map[string]int    `json:"message_stats"` // 按内容统计（简化版）
	TimeStats    map[string]int    `json:"time_stats"`    // 按时间段统计
}

type IPStat struct {
	Total          int            `json:"total"`
	LevelBreakdown map[string]int `json:"level_breakdown"`
	TypeBreakdown  map[string]int `json:"type_breakdown"`
}

type AggregatedStream struct {
	StreamName string
	StatDate   string
	TotalFiles int64
	TotalSize  int64
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
	// 全局并发控制（限制同时对外HTTP请求的并发数）
	outboundSem = make(chan struct{}, 30) // <= tunable: e.g. 50 concurrent external requests
	// 电路断路 / 失败冷却（address -> lastFailedTime）
	lastFailure       sync.Map // map[string]time.Time
	pingFailures      sync.Map // key: ip -> time.Time (last failure)
	portFailures      sync.Map // key: "ip:port" -> time.Time (last failure)
	alertHistory      []AlertRecord
	alertHistoryMutex sync.Mutex
)

const (
	maxWorkers        = 500 // 最大工作协程数
	checkCacheTTL     = 10  // 检查结果缓存时间(秒)
	resourceCheckFreq = 90  // 资源监控频率(秒)
	portCheckFreq     = 10  // 端口监控频率(秒)
	pingCheckFreq     = 60  // Ping监控频率(秒)
	processCheckFreq  = 3   // 进程监控频率(秒)
	targetPortFreq    = 30  // 目标端口监控频率(秒)
	// 失败冷却时长：若地址在该时长内失败过，则短路跳过实际请求
)

// 主函数
// author: houshenghai
// date: 2025-02-30
func main() {
	// 1. 初始化路径与模版
	projectRoot, err := getProjectRoot()
	if err != nil {
		fmt.Printf("Error determining project root: %v\n", err)
		return
	}
	templatesDir := filepath.Join(projectRoot, "templates")
	if err := initTemplates(templatesDir); err != nil {
		log.Fatalf("Failed to initialize email templates: %v", err)
	}

	configPath = filepath.Join(projectRoot, "conf", "config.json")

	// 2. 加载配置
	if err := reloadConfig(); err != nil {
		log.Fatalf("Failed to load initial configuration: %v", err)
	}
	// 3. 加载历史告警 (用于持久化)
	LoadAlertHistory() // 调用 report_summary 中定义的函数

	go startConfigWatcher()

	// 初始化全局状态变量
	statuses := make(map[string]*ServerStatus)

	// 4. 初始化邮件发送
	initSMTPPool(config.Email, 5) // 连接池大小5
	initEmailQueue(3)             // 3个邮件发送协程

	defer emailQueue.Close()

	// *** 新增：启动汇总报告调度器 ***
	if config.SummaryReport.Enabled {
		go startReportScheduler()
	}
	go startStreamReportScheduler()

	// 5. 初始化定时器
	portTicker := time.NewTicker(time.Duration(portCheckFreq) * time.Second)
	processTicker := time.NewTicker(time.Duration(processCheckFreq) * time.Second)
	resourceTicker := time.NewTicker(time.Duration(resourceCheckFreq) * time.Second)
	pingTicker := time.NewTicker(time.Duration(pingCheckFreq) * time.Second)
	//dirTicker := time.NewTicker(time.Duration(dirCheckFreq) * time.Second)
	targetPortTicker := time.NewTicker(time.Duration(targetPortFreq) * time.Second)

	// defer portTicker.Stop()
	// defer processTicker.Stop()
	// defer resourceTicker.Stop()
	// defer pingTicker.Stop()
	// defer targetPortTicker.Stop()

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

	// 6. [新增] 创建退出信号通道
	quitChan := make(chan os.Signal, 1)
	signal.Notify(quitChan, syscall.SIGINT, syscall.SIGTERM)

	// 7. 启动主循环 (非阻塞)
	go func() {
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
			case <-targetPortTicker.C:
				go runTargetPortChecks(config, statuses)
				// 监听退出信号
			case <-quitChan:
				log.Println("接收到退出信号，正在保存数据并停止...")
				// [重要] 退出前保存历史记录
				saveAlertHistory()

				// 停止 Ticker
				portTicker.Stop()
				processTicker.Stop()
				resourceTicker.Stop()
				pingTicker.Stop()
				targetPortTicker.Stop()

				log.Println("服务已安全停止")
				os.Exit(0)
			}
		}
	}()
	// 阻塞主 Goroutine，防止程序直接退出
	// 这里使用 select{} 或者等待 quitChan 都可以，
	// 但因为上面的 select 在 goroutine 里，这里我们需要一个阻塞机制
	select {}

}

func init() {
	// 创建自定义Transport实现连接池
	transport := &http.Transport{
		MaxIdleConns:        300,                                   // 最大空闲连接
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

var lastCooldownLog sync.Map // 记录每个端口上次输出冷却日志时间
// 检查端口状态，支持 TCP 和 UDP（带冷却）
// address: ip, port: string
// 并行端口检测，支持快速失败 + 冷却
func checkPort(address, port string) bool {
	addrKey := net.JoinHostPort(address, port)

	// ===== 冷却检测 =====
	if v, ok := portFailures.Load(addrKey); ok { // 存在冷却记录
		if t, ok2 := v.(time.Time); ok2 { //
			elapsed := time.Since(t)
			cooldown := time.Duration(config.PortCoolDown) * time.Second
			if elapsed < cooldown {
				remaining := cooldown - elapsed

				// 控制日志输出频率（例如每60秒打印一次）
				const cooldownLogInterval = 60 * time.Second
				if last, ok := lastCooldownLog.Load(addrKey); !ok || time.Since(last.(time.Time)) > cooldownLogInterval {
					log.Printf("[ monitorPorts ] %s in cooldown (%.0fs remaining), skipping TCP/UDP check",
						addrKey, remaining.Seconds())
					lastCooldownLog.Store(addrKey, time.Now())
				}
				return false // 冷却期内跳过实际检测
				// 冷却过期后继续检测
			}
			portFailures.Delete(addrKey)
			lastCooldownLog.Delete(addrKey)
			log.Printf("[ monitorPorts ] %s cooldown expired, resuming TCP/UDP check", addrKey)
		}
	}

	var tcpOK, udpOK bool
	var wg sync.WaitGroup
	wg.Add(2)

	// ===== TCP 并行检测 =====
	go func() {
		defer wg.Done()
		const tcpMaxRetries = 3
		const retryDelay = 500 * time.Millisecond
		var tcpTimeout = time.Duration(config.TcpPortDetectTimeout) * time.Millisecond

		for attempt := 1; attempt <= tcpMaxRetries; attempt++ {
			conn, err := net.DialTimeout("tcp", addrKey, tcpTimeout)
			if err == nil {
				conn.Close()
				tcpOK = true
				return
			}
			time.Sleep(retryDelay)
		}
	}()

	// ===== UDP 并行检测 =====
	go func() {
		defer wg.Done()
		const udpMaxRetries = 3
		const udpTimeout = 800 * time.Millisecond
		var udpRetryDelay = time.Duration(config.UdpPortDetectTimeout) * time.Millisecond

		udpAddr, err := net.ResolveUDPAddr("udp", addrKey)
		if err != nil {
			return
		}

		for i := 0; i < udpMaxRetries; i++ {
			if checkUDP(udpAddr, udpTimeout) {
				udpOK = true
				return
			}
			time.Sleep(udpRetryDelay)
		}
	}()

	// ===== 等待检测完成或超时 =====
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Duration(config.TcpAndUdpResultAddTime) * time.Second): // 总超时（防止网络阻塞导致长时间挂起）
		log.Printf("[ monitorPorts ] %s check timeout after %ds", addrKey, config.TcpAndUdpResultAddTime)
	}

	// ===== 结果判断 =====
	if tcpOK || udpOK {
		// 成功恢复清除冷却状态
		if _, ok := portFailures.Load(addrKey); ok {
			log.Printf("[ monitorPorts ] %s recovered, leaving cooldown", addrKey)
			portFailures.Delete(addrKey)
		}
		return true
	}

	// ===== TCP & UDP 都失败 =====
	now := time.Now()
	if _, ok := portFailures.Load(addrKey); !ok {
		log.Printf("[ monitorPorts ] %s TCP and UDP both failed, entering cooldown %ds",
			addrKey, config.PortCoolDown)
	}
	portFailures.Store(addrKey, now)
	//log.Printf("服务器 %s 端口 %s TCP 和 UDP 均无法通信，最终检测失败。", address, port)
	return false
}

// 检查 UDP 端口状态，发送测试数据包来测试返回连通性
func checkUDP(udpAddr *net.UDPAddr, timeout time.Duration) bool {
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
	// 设置读取超时时间
	udpConn.SetReadDeadline(time.Now().Add(timeout))

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

// 检查服务器通信状态，是否可以ping通（带冷却机制）
// 原理：失败进入冷却期，冷却期间跳过检测；成功后清除冷却状态。
func checkPingState(address string) bool {
	// ---------- 冷却检查 ----------
	if v, ok := pingFailures.Load(address); ok {
		if t, ok2 := v.(time.Time); ok2 {
			if time.Since(t) < time.Duration(config.PingCoolDown)*time.Second {
				// 冷却中，直接跳过
				if time.Since(t) < 5*time.Second { // 只打印一次
					log.Printf("[ monitorPing ] %s in cooldown, skipping ping check", address)
				}
				return false
			}
		}
	}
	// 确定使用的端口，如果配置为 0 则默认 22
	targetSshPort := config.SshPort
	if targetSshPort <= 0 {
		targetSshPort = 22
	}
	// ---------- 确定检测策略 ----------
	useICMP := config.EnableIcmpPing
	useTCP := config.EnableTcpPing
	// 兼容逻辑：如果两个都没配置（旧配置文件），默认使用原来的 TCP 检测
	if !useICMP && !useTCP {
		useTCP = true
	}

	// ---------- 开始 Ping 检查 ----------
	const maxPingRetries = 2
	const waitBetweenPingRetries = 1 * time.Second
	successPing := true

	// ---------- 1. 执行 ICMP 检测 ----------
	if useICMP {
		icmpOK := false
		for retry := 0; retry <= maxPingRetries; retry++ {
			if retry > 0 {
				time.Sleep(waitBetweenPingRetries)
				//log.Printf("%s [ICMP] 地址 %s 检测失败，第 %d 次重试...", time.Now().Format("2006-01-02 15:04:05"), address, retry)
			}
			if checkICMP(address, config.IcmpTimeout) {
				icmpOK = true
				break
			}
		}
		if !icmpOK {
			log.Printf("[ monitorPing ] %s ICMP ping failed", address)
			successPing = false
		}
	}

	// ---------- 2. 执行 TCP 检测 (仅当 ICMP 通过或未启用时才继续，否则直接失败) ----------
	// 逻辑：如果 ICMP 已经失败，且要求"任一失败即失联"，则无需再测 TCP，结果已经是 false
	// 但为了日志完整性，这里可以选择继续测 TCP。
	const maxRetries = 2
	const waitBetweenRetries = 1 * time.Second
	successTcp := true

	if useTCP {
		tcpOK := false
		for retry := 0; retry <= maxRetries; retry++ {
			if retry > 0 {
				time.Sleep(waitBetweenRetries)
				// log.Printf("%s [TCP] 地址 %s 检测失败，第 %d 次重试...", time.Now().Format("2006-01-02 15:04:05"), address, retry)
			}
			if checkTCP22(address, targetSshPort, config.PortTimeout) {
				tcpOK = true
				break
			}
		}
		if !tcpOK {
			log.Printf("[ monitorPing ] %s TCP ping (port 22) failed", address)
			successTcp = false
		}
	}

	// ---------- 结果处理 ----------
	if (useICMP && !successPing) || (useTCP && !successTcp) {
		now := time.Now()
		if _, existed := pingFailures.Load(address); !existed {
			log.Printf("[ monitorPing ] %s unreachable (Strategy: ICMP=%v, TCP=%v), entering cooldown for %v",
				address, useICMP, useTCP, time.Duration(config.PingCoolDown)*time.Second)
		}
		pingFailures.Store(address, now)
		return false
	}

	// ---------- 恢复处理 ----------
	if _, inCooldown := pingFailures.Load(address); inCooldown {
		log.Printf("[ monitorPing ] %s recovered, leaving cooldown", address)
		pingFailures.Delete(address)
	}

	return true
}

// [v2.5.0新增] 底层 ICMP 检测函数 (调用系统 ping)
func checkICMP(address string, timeoutSeconds int) bool {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 3 // 默认超时3秒
	}
	// Linux 下 ping 使用 -c 次数, -W 超时(秒)
	cmd := exec.Command("ping", "-c", "2", "-W", strconv.Itoa(timeoutSeconds), address)

	// 禁用标准输出，避免日志杂乱
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Run()
	return err == nil
}

// [v2.5.0新增] 底层 TCP 22端口检测函数
func checkTCP22(address string, port int, timeoutSeconds int) bool {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, strconv.Itoa(port)), time.Duration(timeoutSeconds)*time.Second)
	if err == nil {
		conn.Close()
		return true
	}
	return false
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

// 发送告警（根据级别选择发送渠道）
func sendAlert(alertLevel string, data EmailTemplateData) {
	data = enrichAlertData(data)
	recordAlert(alertLevel, data)

	// 根据告警级别选择发送渠道

	// 判断发送邮件
	if config.AlertMethods.Email {
		if emailQueue != nil {
			emailQueue.AddTask(config.Email, alertLevel, data)
		} else {
			sendEmail(config.Email, alertLevel, data)
		}
	} else {
		// 如果企业微信也禁用，则记录日志
		log.Printf("邮件告警通知被禁用，仅记录日志: %s - %s", data.Subject, data.Message)
	}

	// 判断发送企业微信
	if config.AlertMethods.WechatWork {
		sendWechatWorkAlert(config.WechatWork, alertLevel, data)
	} else {
		// 如果企业微信也禁用，则记录日志
		log.Printf("企业微信告警通知被禁用，仅记录日志: %s - %s", data.Subject, data.Message)
	}

	// 如果两种方式都禁用，则记录日志
	if !config.AlertMethods.Email && !config.AlertMethods.WechatWork {
		log.Printf("邮件与企业微信告警通知均被禁用，仅记录日志: %s - %s", data.Subject, data.Message)
	}

}

func enrichAlertData(data EmailTemplateData) EmailTemplateData {
	if data.Datacenter == "" {
		if dc := lookupDatacenterByAddress(data.Server); dc != "" {
			data.Datacenter = dc
		} else {
			data.Datacenter = "未配置"
		}
	}
	return data
}

func lookupDatacenterByAddress(address string) string {
	configMutex.RLock()
	defer configMutex.RUnlock()

	for _, server := range config.Servers {
		for _, addr := range server.Addresses {
			if addr == address {
				return server.Datacenter
			}
		}
	}
	return ""
}

// parseReportTime 解析 "HH:MM" 格式的时间字符串为今天的 time.Time
func parseReportTime(timeStr string) time.Time {
	layout := "15:04"
	// 尝试解析
	parsed, err := time.Parse(layout, timeStr)
	if err != nil {
		log.Printf("解析报告时间格式错误 (%s): %v, 将使用默认时间 08:00", timeStr, err)
		// 解析失败则默认 08:00
		parsed, _ = time.Parse(layout, "08:00")
	}

	// time.Parse 默认返回的是公元0年的时间，我们需要结合今天的日期
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, now.Location())
}

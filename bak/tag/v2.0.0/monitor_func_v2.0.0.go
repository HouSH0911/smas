package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 主监控函数
func unifiedMonitor(config Config, statuses map[string]*ServerStatus) {
	configMutex.RLock()
	currentConfig := config
	configMutex.RUnlock()

	if !anyMonitoringEnabled(currentConfig.Monitor) { // 检查是否有任何监控功能被启用
		log.Println("All monitoring is disabled")
		return
	}

	//log.Println("Starting unified monitoring")
	startTime := time.Now()
	// 每个功能进入前打日志
	if currentConfig.Monitor.ServerReachMonitor {
		log.Println("[ monitorPing ] function starts")
	}
	if currentConfig.Monitor.PortMonitor {
		log.Println("[ monitorPorts ] function starts")
	}
	if currentConfig.Monitor.ProcessMonitor {
		log.Println("[ monitorProcesses ] function starts")
	}
	if currentConfig.Monitor.Resource {
		log.Println("[ monitorResources ] function starts")
	}
	if currentConfig.Monitor.DirFileMonitor {
		log.Println("[ monitorDirectories ] function starts")
	}
	if currentConfig.Monitor.RaPortMonitor {
		log.Println("[ monitorTargetPorts ] function starts")
	}

	// 创建工作池
	jobChan := make(chan serverJob, maxWorkers*2) // 缓冲区大小为 maxWorkers 的两倍
	var wg sync.WaitGroup

	// 启动worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				processServer(job, statuses, currentConfig)
			}
		}()
	}

	// 分发任务
	jobsCount := 0
	for _, server := range currentConfig.Servers {
		for _, address := range server.Addresses {
			for _, port := range server.Ports {
				if strings.TrimSpace(port) == "" { // ✅ 跳过空端口
					continue
				}
				key := fmt.Sprintf("%s:%s", address, port)
				jobChan <- serverJob{
					address: address,
					port:    port,
					server:  server,
					key:     key,
				}
				jobsCount++
			}
		}
	}
	close(jobChan)
	wg.Wait()
	if currentConfig.Monitor.ServerReachMonitor {
		log.Println("[ monitorPing ] function completed")
	}
	if currentConfig.Monitor.PortMonitor {
		log.Println("[ monitorPorts ] function completed")
	}
	if currentConfig.Monitor.ProcessMonitor {
		log.Println("[ monitorProcesses ] function completed")
	}
	if currentConfig.Monitor.Resource {
		log.Println("[ monitorResources ] function completed")
	}
	if currentConfig.Monitor.DirFileMonitor {
		log.Println("[ monitorDirectories ] function completed")
	}
	if currentConfig.Monitor.RaPortMonitor {
		log.Println("[ monitorTargetPorts ] function completed")
	}
	log.Printf("Unified monitoring completed. Servers: %d, Time: %v",
		jobsCount, time.Since(startTime))
}

func anyMonitoringEnabled(monitor MonitorConfig) bool {
	return monitor.ProcessMonitor ||
		monitor.PortMonitor ||
		monitor.ServerReachMonitor ||
		monitor.Resource ||
		monitor.DirFileMonitor ||
		monitor.RaPortMonitor
}

func processServer(job serverJob, statuses map[string]*ServerStatus, config Config) {
	// 1. 检查服务器连通性
	pingState := checkPingState(job.address)

	// 初始化状态
	statusesMutex.Lock()
	if statuses[job.key] == nil {
		statuses[job.key] = &ServerStatus{
			PortAlertSent:       0,
			ProcessAlertSent:    make(map[string]int32),
			CpuAlertSent:        0,
			MemAlertSent:        0,
			DiskAlertSent:       make(map[string]int32),
			DirAlertSent:        make(map[string]int32),
			FileAlertSent:       make(map[string]int32),
			PingAlertSent:       0,
			TargetPortAlertSent: make(map[string]int32),
		}
	}
	status := statuses[job.key]
	statusesMutex.Unlock()

	// 处理Ping状态

	handlePingStatus(job.address, job.key, pingState, status, config)
	if !pingState {
		return // 服务器不可达，跳过其他检查
	}

	// 2. 检查端口状态
	if config.Monitor.PortMonitor && shouldCheck(job.key, "port", portCheckFreq) {
		portState := checkPort(job.address, job.port)
		handlePortStatus(job.address, job.port, job.key, portState, status, config)

	}

	// 3. 获取/check数据 (带缓存)
	statusResponse, err := getCachedCheckData(job.address)
	if err != nil {
		log.Printf("Error getting check data for %s: %v", job.address, err)
		return
	}

	now := time.Now()

	// 4. 处理进程状态
	if config.Monitor.ProcessMonitor && shouldCheck(job.key, "process", processCheckFreq) {
		handleProcessStatus(job.address, job.key, statusResponse.ProcessStatuses, status, config)

	}
	time.Sleep(time.Millisecond * 2000) // 短暂休眠，避免过快处理

	// 5. 处理资源状态
	if config.Monitor.Resource && shouldCheck(job.key, "resource", resourceCheckFreq) {
		handleResourceStatus(job.address, job.key, statusResponse.Metrics, job.server, status, config)
	}
	time.Sleep(time.Millisecond * 1000)

	// 6. 处理目录状态
	if now.Minute()%5 == 2 {
		if config.Monitor.DirFileMonitor && shouldCheck(job.key, "dir", dirCheckFreq) {
			handleDirectoryStatus(job.address, job.key, statusResponse.DirectoryStatuses, status, config, now)
		}
	}

	// 7. 处理目标端口状态
	if config.Monitor.RaPortMonitor && shouldCheck(job.key, "targetport", targetPortFreq) {
		handleTargetPortStatus(job.address, job.key, statusResponse.PortStatuses, status, config)
	}
}

// 获取带缓存的检查数据
func getCachedCheckData(address string) (*StatusResponse, error) {
	// 检查缓存
	if cached, ok := cachedChecks.Load(address); ok {
		if lastTime, ok := lastCheckTime.Load(address); ok {
			if time.Since(lastTime.(time.Time)) < time.Second*time.Duration(checkCacheTTL) {
				return cached.(*StatusResponse), nil
			}
		}
	}

	// 调用API获取新数据
	url := fmt.Sprintf("http://%s:9600/check", address)
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 解析响应
	var statusResponse StatusResponse
	if err := json.Unmarshal(bodyData, &statusResponse); err != nil {
		return nil, err
	}

	// 更新缓存
	cachedChecks.Store(address, &statusResponse)
	lastCheckTime.Store(address, time.Now())

	return &statusResponse, nil
}

// 检查是否应该执行某项检查
func shouldCheck(key, checkType string, freq int) bool {
	lastKey := fmt.Sprintf("%s_%s", key, checkType)
	last, ok := lastCheckTime.Load(lastKey)
	if !ok {
		return true
	}
	return time.Since(last.(time.Time)) > time.Duration(freq)*time.Second
}

// 处理Ping状态
func handlePingStatus(address, key string, pingState bool, status *ServerStatus, config Config) {
	if !config.Monitor.ServerReachMonitor {
		return
	}

	pingAlertSent := atomic.LoadInt32(&status.PingAlertSent) == 1

	if !pingState && !pingAlertSent {
		data := EmailTemplateData{
			Subject:   "🚨服务器失联告警",
			Server:    address,
			Message:   "服务器通信失联，如影响业务请及时处理！",
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "critical", data)
		log.Printf("服务器 %s 通过ping检测通信失联，请确认告警邮件已发送", address)
		atomic.StoreInt32(&status.PingAlertSent, 1)
		//fmt.Println(data.Message)
	} else if pingState && pingAlertSent {
		data := EmailTemplateData{
			Subject:   "✅服务器通信恢复",
			Server:    address,
			Message:   "服务器通信已恢复正常，请检查所承载业务是否已正常启动！",
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "recovery", data)
		log.Printf("服务器 %s 通过ping检测通信已恢复，请确认恢复邮件已发送", address)
		atomic.StoreInt32(&status.PingAlertSent, 0)
		//fmt.Println(data.Message)
	}

}

// 处理端口状态
func handlePortStatus(address, port, key string, portState bool, status *ServerStatus, config Config) {
	portAlertSent := atomic.LoadInt32(&status.PortAlertSent) == 1

	if !portState && !portAlertSent {
		data := EmailTemplateData{
			Subject:   "⚠️⚠️端口失联告警",
			Server:    address,
			Message:   fmt.Sprintf("服务器端口 %s 通信失联", port),
			Action:    "请检查端口相关的进程是否有存在，或者是否有重启现象！",
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "severe", data)
		atomic.StoreInt32(&status.PortAlertSent, 1)
		log.Printf("服务器 %s 端口 %s 通过tcp检测端口失联，请确认告警邮件已发送", address, port)
		//fmt.Println(data.Message)
	} else if portState && portAlertSent {
		data := EmailTemplateData{
			Subject:   "✅端口通信恢复",
			Server:    address,
			Message:   fmt.Sprintf("服务器端口 %s 通信已恢复正常。", port),
			Action:    "服务器端口已恢复正常，请知悉！",
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "recovery", data)
		log.Printf("服务器 %s 端口 %s 通过tcp检测端口通信恢复，请确认恢复邮件已发送", address, port)
		atomic.StoreInt32(&status.PortAlertSent, 0)
		//fmt.Println(data.Message)
	}
}

// 处理进程状态
func handleProcessStatus(address, key string, processes []ProcessStatus, status *ServerStatus, config Config) {
	for _, result := range processes {
		processName := result.ProcessName
		isRunning := result.IsRunning

		// 使用原子操作管理状态
		statusMap := &status.ProcessAlertSent
		statusMutex := &status.ProcessMutex

		statusMutex.Lock()
		alertSent, exists := (*statusMap)[processName]
		if !exists {
			(*statusMap)[processName] = 0
			alertSent = 0
		}

		if !isRunning && alertSent == 0 {
			data := EmailTemplateData{
				Subject:   "⚠️⚠️进程消失告警",
				Server:    address,
				Message:   fmt.Sprintf("服务器进程 %s 不存在，请检查！", processName),
				Action:    "请登录服务器检查进程是否存在，或者是否有重启现象！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "severe", data)
			(*statusMap)[processName] = 1
			log.Printf("服务器 %s 进程 %s 不存在，请确认告警邮件已发送", address, processName)
			//fmt.Println(data.Message)
		} else if isRunning && alertSent == 1 {
			data := EmailTemplateData{
				Subject:   "✅进程已启动",
				Server:    address,
				Message:   fmt.Sprintf("服务器进程 %s 已启动！", processName),
				Action:    "进程已启动，请观察是否有异常！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "recovery", data)
			(*statusMap)[processName] = 0
			log.Printf("服务器 %s 进程 %s 已启动，请确认恢复邮件已发送", address, processName)
			//fmt.Println(data.Message)
		}
		statusMutex.Unlock()
	}
}

// 处理资源状态
func handleResourceStatus(address, key string, metrics Metrics, server Server, status *ServerStatus, config Config) {
	// 原子操作管理状态
	cpuAlertSent := atomic.LoadInt32(&status.CpuAlertSent) == 1
	memAlertSent := atomic.LoadInt32(&status.MemAlertSent) == 1

	if metrics.CPUUsage > server.CPUThreshold && !cpuAlertSent {
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
		log.Printf("服务器 %s CPU利用率已达到 %s 超过阈值，请确认告警邮件已发送",
			address, metrics.CPUUsage)
		atomic.StoreInt32(&status.CpuAlertSent, 1)
	} else if metrics.CPUUsage < server.CPUThreshold && cpuAlertSent {
		data := EmailTemplateData{
			Subject:   "✅CPU使用率已降低",
			Server:    address,
			Message:   "CPU使用率已降低，恢复正常",
			Value:     fmt.Sprintf("%.2f%%", metrics.CPUUsage),
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "recovery", data)
		log.Printf("服务器 %s CPU利用率已降到 %s ，请确认恢复邮件已发送",
			address, metrics.CPUUsage)
		atomic.StoreInt32(&status.CpuAlertSent, 0)
	}

	if metrics.MemoryUsage > server.MemoryThreshold && !memAlertSent {
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
		log.Printf("服务器 %s 内存利用率已达到 %s 超过阈值，请确认告警邮件已发送",
			address, metrics.MemoryUsage)
		atomic.StoreInt32(&status.MemAlertSent, 1)
	} else if metrics.MemoryUsage < server.MemoryThreshold && memAlertSent {
		data := EmailTemplateData{
			Subject:   "✅内存使用率已降低",
			Server:    address,
			Message:   "服务器内存使用率已降低，恢复正常！",
			Value:     fmt.Sprintf("%.2f%%", metrics.MemoryUsage),
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendEmail(config.Email, "recovery", data)
		log.Printf("服务器 %s 内存利用率已降到 %s ，请确认恢复邮件已发送",
			address, metrics.MemoryUsage)
		atomic.StoreInt32(&status.MemAlertSent, 0)
	}

	// 磁盘状态处理
	diskMutex := &status.DiskMutex
	diskMap := &status.DiskAlertSent

	diskMutex.Lock()

	for mountpoint, usage := range metrics.DiskUsage {
		if isExcludedMountPoint(mountpoint, server.ExcludeMountPoints) {
			continue
		}

		alertSent, exists := (*diskMap)[mountpoint]
		if !exists {
			(*diskMap)[mountpoint] = 0
			alertSent = 0
		}

		if usage > server.DiskThreshold && alertSent == 0 {
			data := EmailTemplateData{
				Subject:   "⚠️磁盘使用率告警",
				Server:    address,
				Message:   "磁盘利用率超过阈值！",
				Value:     fmt.Sprintf("%.2f%% 挂载点：%s", usage, mountpoint),
				Threshold: fmt.Sprintf("%.2f%%", server.DiskThreshold),
				Action:    "请检查服务器磁盘分区使用情况，必要时进行清理或扩容！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "warning", data)
			log.Printf("服务器 %s 挂载点 %s 利用率已达到 %s 超过阈值，请确认告警邮件已发送",
				address, mountpoint, usage)
			(*diskMap)[mountpoint] = 1
		} else if usage < server.DiskThreshold && alertSent == 1 {
			data := EmailTemplateData{
				Subject:   "✅磁盘利用率已降低",
				Server:    address,
				Message:   "服务器磁盘利用率已降低，恢复正常！",
				Value:     fmt.Sprintf("%.2f%% 挂载点：%s", usage, mountpoint),
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "recovery", data)
			log.Printf("服务器 %s 挂载点 %s 利用率已降到 %s ，请确认恢复邮件已发送",
				address, mountpoint, usage)
			(*diskMap)[mountpoint] = 0
		}
	}
	diskMutex.Unlock()
}

// 处理目录状态
func handleDirectoryStatus(address, key string, dirs []DirectoryStatus, status *ServerStatus, config Config, now time.Time) {
	dirMutex := &status.DirMutex
	dirMap := &status.DirAlertSent
	fileMap := &status.FileAlertSent

	dirMutex.Lock()
	defer dirMutex.Unlock()

	for _, result := range dirs {
		baseDir := result.BaseDir
		folderExists := result.DirectoryExist
		fileActuallyExists := result.XdrFileExist // ✅ 使用接口返回的文件存在状态

		// 初始化目录告警状态
		dirAlertSent, ok1 := (*dirMap)[baseDir]
		if !ok1 {
			(*dirMap)[baseDir] = 0
			dirAlertSent = 0
		}

		// 初始化文件告警状态
		fileAlertSent, ok2 := (*fileMap)[baseDir]
		if !ok2 {
			(*fileMap)[baseDir] = 0
			fileAlertSent = 0
		}

		// 1. 目录/文件缺失告警
		if !folderExists && !fileActuallyExists && dirAlertSent == 0 {
			data := EmailTemplateData{
				Subject:   "⚠️⚠️目录/文件缺失告警",
				Server:    address,
				Message:   fmt.Sprintf("服务器目录 %s 不存在指定目录或文件！", baseDir),
				Action:    "请检查服务器输出文件进程是否正常运行！",
				Timestamp: now.Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "warning", data)
			log.Printf("服务器 %s 目录 %s 不存在指定目录/文件，请确认告警邮件已发送",
				address, baseDir)
			(*dirMap)[baseDir] = 1
		} else if (folderExists || fileActuallyExists) && dirAlertSent == 1 {
			data := EmailTemplateData{
				Subject:   "✅目录/文件已恢复",
				Server:    address,
				Message:   fmt.Sprintf("服务器目录 %s 文件/目录已恢复！", baseDir),
				Timestamp: now.Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "recovery", data)
			log.Printf("服务器 %s 目录 %s 目录/文件已存在，请确认恢复邮件已发送",
				address, baseDir)
			(*dirMap)[baseDir] = 0
		}

		// 2. 文件缺失告警（目录存在但文件缺失）
		if folderExists {
			if !fileActuallyExists && fileAlertSent == 0 {
				data := EmailTemplateData{
					Subject:   "⚠️⚠️文件缺失告警",
					Server:    address,
					Message:   fmt.Sprintf("服务器目录 %s 不存在指定文件！", baseDir),
					Action:    "请检查服务器输出或转移文件进程是否正常运行！",
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "warning", data)
				log.Printf("服务器 %s 目录 %s 不存在指定文件，请确认告警邮件已发送",
					address, baseDir)
				(*fileMap)[baseDir] = 1
			} else if fileActuallyExists && fileAlertSent == 1 {
				data := EmailTemplateData{
					Subject:   "✅目录文件已恢复",
					Server:    address,
					Message:   fmt.Sprintf("服务器目录 %s 内文件已恢复！", baseDir),
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendEmail(config.Email, "recovery", data)
				log.Printf("服务器 %s 目录 %s 文件已存在，请确认恢复邮件已发送",
					address, baseDir)
				(*fileMap)[baseDir] = 0
			}
		}
	}
}

// 处理目标端口状态
func handleTargetPortStatus(address, key string, ports []PortStatus, status *ServerStatus, config Config) {
	portMutex := &status.TargetPortMutex
	portMap := &status.TargetPortAlertSent

	portMutex.Lock()
	defer portMutex.Unlock()

	for _, result := range ports {
		hostName := result.Host
		targetPort := result.Port
		portState := result.Status

		alertSent, exists := (*portMap)[hostName]
		if !exists {
			(*portMap)[hostName] = 0
			alertSent = 0
		}

		if !portState && alertSent == 0 {
			data := EmailTemplateData{
				Subject:   "⚠️目标服务器与下游端口失联告警",
				Server:    address,
				Message:   fmt.Sprintf("从服务器 %s 到目标服务器 %s 的 %d 端口通信失联，请核查！", address, hostName, targetPort),
				Action:    "请登录服务器检查端口通信是否正常，否则影响相关通信传输！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "severe", data)
			log.Printf("服务器 %s 访问 %s 的端口 %s 不通，请确认告警邮件已发送",
				address, hostName, targetPort)
			(*portMap)[hostName] = 1
		} else if portState && alertSent == 1 {
			data := EmailTemplateData{
				Subject:   "✅目标服务器与下游端口通信恢复",
				Server:    address,
				Message:   fmt.Sprintf("从服务器 %s 到目标服务器 %s 的 %d 端口通信已恢复正常。", address, hostName, targetPort),
				Action:    "与目标服务器的端口通信已恢复正常，请知悉！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "recovery", data)
			log.Printf("从服务器 %s 到 %s 的端口 %s 通信恢复，请确认恢复邮件已发送",
				address, hostName, targetPort)
			(*portMap)[hostName] = 0
		}
	}
}

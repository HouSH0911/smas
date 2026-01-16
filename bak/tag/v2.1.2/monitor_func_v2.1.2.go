package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// runPortChecks 负责独立执行端口监控
func runPortChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.PortMonitor {
		return
	}
	if !portRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorPorts ] already running, skip this round")
		return
	}
	startTime := time.Now()

	log.Println("[ monitorPorts ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorPorts ] function completed (duration: %.4fs)", duration.Seconds())
		portRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				// 只执行端口检查相关逻辑
				pingState := checkPingState(job.address)
				if !pingState {
					continue // 服务器不通，跳过
				}
				portState := checkPort(job.address, job.port)

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handlePortStatus(job.address, job.port, job.key, portState, status, config)
			}
		}()
	}

	// 分发任务
	distributePortJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorPorts ] function completed")
}

// runProcessChecks 负责独立执行进程监控
func runProcessChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.ProcessMonitor {
		return
	}
	if !processRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorProcesses ] already running, skip this round")
		return
	}
	startTime := time.Now()
	log.Println("[ monitorProcesses ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorProcesses ] function completed (duration: %.4fs)", duration.Seconds())
		processRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				// 只执行进程检查相关逻辑
				pingState := checkPingState(job.address)
				if !pingState {
					continue // 服务器不通，跳过
				}
				statusResponse, err := getCachedCheckData(job.address)
				if err != nil {
					//log.Printf("Error getting check data for %s: %v", job.address, err)
					continue
				}

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handleProcessStatus(job.address, job.key, statusResponse.ProcessStatuses, status, config)
			}
		}()
	}

	// 分发任务
	distributeAddressJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorProcesses ] function completed")
}

// runPingChecks 负责独立执行服务器通信状态监控
func runPingChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.ServerReachMonitor {
		return
	}
	if !pingRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorPorts ] already running, skip this round")
		return
	}
	startTime := time.Now()

	log.Println("[ monitorPing ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorPing ] function completed (duration: %.4fs)", duration.Seconds())
		pingRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				pingState := checkPingState(job.address)

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handlePingStatus(job.address, job.key, pingState, status, config)
			}
		}()
	}

	// 分发任务
	distributeAddressJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorPing ] function completed")
}

// runResourceChecks 负责独立执行服务器资源状态监控
func runResourceChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.Resource {
		return
	}
	if !resourceRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorPorts ] already running, skip this round")
		return
	}
	startTime := time.Now()

	log.Println("[ monitorResource ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorResource ] function completed (duration: %.4fs)", duration.Seconds())
		resourceRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				pingState := checkPingState(job.address)
				if !pingState {
					continue // 服务器不通，跳过
				}
				statusResponse, err := getCachedCheckData(job.address)
				if err != nil {
					//log.Printf("Error getting check data for %s: %v", job.address, err)
					continue
				}

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handleResourceStatus(job.address, job.key, statusResponse.Metrics, job.server, status, config)
			}
		}()
	}

	// 分发任务
	distributeAddressJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorResource ] function completed")
}

// runDirectoryChecks 负责独立执行服务器资源状态监控
func runDirectoryChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.DirFileMonitor {
		return
	}
	if !directoryRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorPorts ] already running, skip this round")
		return
	}
	startTime := time.Now()

	log.Println("[ monitorDirectory ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorDirectory ] function completed (duration: %.4fs)", duration.Seconds())
		directoryRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)
	now := time.Now()

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				pingState := checkPingState(job.address)
				if !pingState {
					continue // 服务器不通，跳过
				}
				statusResponse, err := getCachedCheckData(job.address)
				if err != nil {
					//log.Printf("Error getting check data for %s: %v", job.address, err)
					continue
				}

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handleDirectoryStatus(job.address, job.key, statusResponse.DirectoryStatuses, status, config, now)
			}
		}()
	}

	// 分发任务
	distributeAddressJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorDirectory ] function completed")
}

// runTargetPortChecks 负责独立执行服务器资源状态监控
func runTargetPortChecks(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.RaPortMonitor {
		return
	}
	if !targetPortRunning.CompareAndSwap(false, true) {
		//log.Println("[ monitorPorts ] already running, skip this round")
		return
	}
	startTime := time.Now()

	log.Println("[ monitorTargetPort ] function starts")
	defer func() {
		duration := time.Since(startTime)
		log.Printf("[ monitorTargetPort ] function completed (duration: %.4fs)", duration.Seconds())
		targetPortRunning.Store(false)
	}()

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				pingState := checkPingState(job.address)
				if !pingState {
					continue // 服务器不通，跳过
				}
				statusResponse, err := getCachedCheckData(job.address)
				if err != nil {
					//log.Printf("Error getting check data for %s: %v", job.address, err)
					continue
				}

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
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
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handleTargetPortStatus(job.address, job.key, statusResponse.PortStatuses, status, config)
			}
		}()
	}

	// 分发任务
	distributeAddressJobs(config, jobChan)
	wg.Wait()
	//log.Println("[ monitorTargetPort ] function completed")
}

// 可选：创建一个辅助函数来分发任务，避免代码重复
func distributePortJobs(config Config, jobChan chan<- serverJob) {
	// 遍历所有服务器、地址和端口来创建任务
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			// 对于进程、资源等非端口特定的监控，我们只需要每个地址一个任务
			// 这里为了简化，我们仍然用 address:port 作为 key，但可以优化
			if len(server.Ports) == 0 { // 如果没有端口，至少为地址创建一个任务
				key := address
				jobChan <- serverJob{address: address, server: server, key: key}
			} else {
				for _, port := range server.Ports {
					if strings.TrimSpace(port) == "" {
						continue
					}
					key := fmt.Sprintf("%s:%s", address, port)
					jobChan <- serverJob{address: address, port: port, server: server, key: key}
				}
			}
		}
	}
	close(jobChan)
}

// distributeAddressJobs 为每个服务器地址创建一个任务
func distributeAddressJobs(config Config, jobChan chan<- serverJob) {
	// 创建一个 map 来防止重复添加相同的地址
	uniqueAddresses := make(map[string]Server)

	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			uniqueAddresses[address] = server
		}
	}

	// 为每个唯一的地址分发一个 job
	for addr, srv := range uniqueAddresses {
		jobChan <- serverJob{address: addr, server: srv, key: addr} // key 直接使用 address
	}
	close(jobChan)
}

// 获取带缓存的检查数据
func getCachedCheckData(address string) (*StatusResponse, error) {
	// 1) 优先使用缓存
	if cached, ok := cachedChecks.Load(address); ok {
		if lastTime, ok := lastCheckTime.Load(address); ok {
			if time.Since(lastTime.(time.Time)) < time.Second*time.Duration(checkCacheTTL) {
				return cached.(*StatusResponse), nil
			}
		}
	}

	// 2) 检查冷却状态
	if val, ok := lastFailure.Load(address); ok {
		if record, ok2 := val.(FailureRecord); ok2 {
			cooldown := time.Duration(config.FailureCooldown) * time.Second
			if time.Since(record.LastFail) < cooldown {
				// 冷却期内，仅首次提示一次
				if !record.Notified {
					log.Printf("Error getting check data for %s: recent failure, short-circuited", address)
					record.Notified = true
					lastFailure.Store(address, record)
				}
				return nil, fmt.Errorf("recent failure, short-circuited: %s", address)
			} else {
				// 冷却过期 → 删除记录
				lastFailure.Delete(address)
			}
		}
	}

	// 3) 并发限制
	select {
	case outboundSem <- struct{}{}:
		defer func() { <-outboundSem }()
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("outbound concurrency busy")
	}

	// 4) 带超时的请求
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.HttpTimeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s:9600/check", address), nil)
	if err != nil {
		lastFailure.Store(address, FailureRecord{LastFail: time.Now(), Notified: false})
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		lastFailure.Store(address, FailureRecord{LastFail: time.Now(), Notified: false})
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		lastFailure.Store(address, FailureRecord{LastFail: time.Now(), Notified: false})
		return nil, fmt.Errorf("bad status: %d, body: %s", resp.StatusCode, string(body))
	}

	// 5) 解析响应
	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		lastFailure.Store(address, FailureRecord{LastFail: time.Now(), Notified: false})
		return nil, err
	}

	var statusResponse StatusResponse
	if err := json.Unmarshal(bodyData, &statusResponse); err != nil {
		lastFailure.Store(address, FailureRecord{LastFail: time.Now(), Notified: false})
		return nil, err
	}

	// 6) 成功 → 清理失败记录 & 更新缓存
	cachedChecks.Store(address, &statusResponse)
	lastCheckTime.Store(address, time.Now())
	lastFailure.Delete(address)

	return &statusResponse, nil
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
		log.Printf("服务器 %s 通过22端口检测通信失联，请确认告警邮件已发送", address)
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
		log.Printf("服务器 %s 通过22端口检测通信已恢复，请确认恢复邮件已发送", address)
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
		log.Printf("服务器 %s 端口 %s 通过 tcp+udp 检测端口失联，请确认告警邮件已发送", address, port)
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
		log.Printf("服务器 %s 端口 %s 通过 tcp+udp 检测端口通信恢复，请确认恢复邮件已发送", address, port)
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
	// 使用互斥保护资源相关状态
	status.ResourceMutex.Lock()
	defer status.ResourceMutex.Unlock()

	// 配置参数（可改为配置项）
	consecutiveToAlert := config.ConsecutiveToAlert     // 连续超过阈值次数才告警
	consecutiveToRecover := config.ConsecutiveToRecover // 连续正常次数才恢复
	emaAlpha := config.ResourceSmooth                   // EMA 平滑系数，0<alpha<=1，alpha 越大响应越快，越小越平滑

	// 获取当前 CPU 值（原始）
	currentCPU := metrics.CPUUsage

	// 如果 LastCPUEMA == 0 则认为未初始化，直接设为当前值
	if status.LastCPUEMA == 0 {
		status.LastCPUEMA = currentCPU
	} else {
		// 指数平滑（EMA）
		status.LastCPUEMA = emaAlpha*currentCPU + (1-emaAlpha)*status.LastCPUEMA
	}

	smoothedCPU := status.LastCPUEMA

	// 使用平滑值进行判断（避免单次 spike）
	cpuThreshold := server.CPUThreshold

	// 超阈值处理（去抖）
	if smoothedCPU > cpuThreshold {
		status.CpuHighCount++
		status.CpuNormalCount = 0
		if status.CpuHighCount >= consecutiveToAlert && !cpuAlertSent {
			data := EmailTemplateData{
				Subject:   "⚠️CPU使用率告警",
				Server:    address,
				Message:   "CPU使用率超过阈值（平滑值）",
				Value:     fmt.Sprintf("%.2f%% (smoothed)", smoothedCPU),
				Threshold: fmt.Sprintf("%.2f%%", cpuThreshold),
				Action:    "请检查服务器负载情况，必要时进行扩容或优化！",
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "warning", data)
			log.Printf("服务器 %s CPU利用率(平滑)=%.2f 超过阈值 %.2f，请确认告警邮件已发送", address, smoothedCPU, cpuThreshold)
			atomic.StoreInt32(&status.CpuAlertSent, 1)
			// 重置计数避免重复发送
			status.CpuHighCount = 0
		}
	} else {
		// 正常值计数（用于恢复）
		status.CpuNormalCount++
		status.CpuHighCount = 0
		if status.CpuNormalCount >= consecutiveToRecover && cpuAlertSent {
			data := EmailTemplateData{
				Subject:   "✅CPU使用率已降低",
				Server:    address,
				Message:   "CPU使用率已恢复正常（平滑值）",
				Value:     fmt.Sprintf("%.2f%% (smoothed)", smoothedCPU),
				Timestamp: time.Now().Format("2006-01-02 15:04:05"),
			}
			sendEmail(config.Email, "recovery", data)
			log.Printf("服务器 %s CPU利用率(平滑)=%.2f 恢复低于阈值 %.2f，请确认恢复邮件已发送", address, smoothedCPU, cpuThreshold)
			atomic.StoreInt32(&status.CpuAlertSent, 0)
			status.CpuNormalCount = 0
		}
	}

	memAlertSent := atomic.LoadInt32(&status.MemAlertSent) == 1

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

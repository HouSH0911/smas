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
	// === v2.5.0新增：构建全局端口->进程查找表 ===
	// 目的：将配置列表转换为 Map
	// Key: 端口号, Value: 需要检查的进程列表
	portToProcessMap := make(map[string][]string)
	for _, mapping := range config.PortProcessMappings {
		for _, p := range mapping.Ports {
			// 将该端口对应的所有进程加入列表
			// 这里可能会有重复，但通常配置不会重复，为了简单直接追加
			portToProcessMap[p] = append(portToProcessMap[p], mapping.Processes...)
		}
	}

	var wg sync.WaitGroup
	jobChan := make(chan serverJob, maxWorkers)

	// 启动 worker
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				if job.server.PortCheck != nil && !*job.server.PortCheck {
					continue
				}
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
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
					}
					// 将新创建的对象存入 map
					statuses[job.key] = status
				}
				statusesMutex.Unlock()

				handlePortStatus(job.address, job.port, job.key, portState, status, config, portToProcessMap)
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
				if job.server.ProcessCheck != nil && !*job.server.ProcessCheck {
					continue
				}
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
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
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
				// *** 新增判断 ***
				if job.server.PingCheck != nil && !*job.server.PingCheck {
					continue
				}
				pingState := checkPingState(job.address)

				// 获取状态对象
				statusesMutex.Lock()
				status, ok := statuses[job.key]
				if !ok { // 如果 map 中不存在这个 key
					// 创建新的 status 对象
					status = &ServerStatus{
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
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
				if job.server.ResourceCheck != nil && !*job.server.ResourceCheck {
					continue
				}
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
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
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
				if job.server.DirectoryCheck != nil && !*job.server.DirectoryCheck {
					continue
				}
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
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
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
				if job.server.TargetPortCheck != nil && !*job.server.TargetPortCheck {
					continue
				}
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
						PortStates: make(map[string]*StateTracker), // [新增]
						//ProcessAlertSent:    make(map[string]int32),
						ProcessStates: make(map[string]*StateTracker),
						CpuAlertSent:  0,
						MemAlertSent:  0,
						DiskAlertSent: make(map[string]int32),
						DirAlertSent:  make(map[string]int32),
						FileAlertSent: make(map[string]int32),
						//PingAlertSent:       0,
						PingState:        &StateTracker{},
						TargetPortStates: make(map[string]*StateTracker),
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

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s:%d/check", address, config.AgentPort), nil)
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
// 处理Ping状态 (支持服务器重启/失联/恢复逻辑)
func handlePingStatus(address, key string, pingState bool, status *ServerStatus, config Config) {
	if !config.Monitor.ServerReachMonitor {
		return
	}

	// 1. 获取配置的窗口时间，默认 180秒
	windowSeconds := config.ServerRestartWindow
	if windowSeconds <= 0 {
		windowSeconds = 600
	}
	window := time.Duration(windowSeconds) * time.Second
	now := time.Now()

	// 获取Ping状态追踪器 (注意：PingState 没有单独的锁，依赖 statusesMutex 或默认并发安全)
	// 如果担心并发，可以复用 status.PingState 上的字段，但 ServerStatus 是针对 key 独立的，通常是安全的。
	tracker := status.PingState
	if tracker == nil {
		tracker = &StateTracker{}
		status.PingState = tracker
	}

	isReachable := pingState

	if !isReachable {
		// === 当前状态：Ping 不通 (DOWN) ===

		if tracker.FirstFailureTime.IsZero() {
			// A. 刚失联：记录时间
			tracker.FirstFailureTime = now
			log.Printf("服务器 %s 通信中断，进入观察期 (%ds)", address, windowSeconds)
		} else {
			// B. 持续失联
			timeSinceDown := now.Sub(tracker.FirstFailureTime)

			if timeSinceDown > window && !tracker.AlertSent {
				// 超过窗口，发送 "失联告警"
				data := EmailTemplateData{
					Subject:   "🚨服务器失联告警",
					Server:    address,
					Message:   fmt.Sprintf("服务器通信失联已超过 %d 秒，如影响业务请及时处理！", windowSeconds),
					Action:    "请前往机房或联系运维检查网络和主机状态！",
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendAlert("critical", data)
				tracker.AlertSent = true
				log.Printf("服务器 %s 确认失联，已发送告警", address)
			}
		}

	} else {
		// === 当前状态：Ping 通了 (UP) ===

		if !tracker.FirstFailureTime.IsZero() {
			// 之前是不通的，现在通了
			timeSinceDown := now.Sub(tracker.FirstFailureTime)

			if tracker.AlertSent {
				// C. 之前发过失联告警 (Confirmed DOWN -> UP) -> 现在是 "恢复"
				data := EmailTemplateData{
					Subject:   "✅服务器通信恢复",
					Server:    address,
					Message:   "服务器通信已恢复正常，请检查所承载业务是否已正常启动！",
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendAlert("recovery", data)
				log.Printf("服务器 %s 通信恢复，已发送邮件", address)

			} else {
				// D. 还没发过告警就通了 (Pending DOWN -> UP) -> 判定为 "服务器重启"
				data := EmailTemplateData{
					Subject:   "🔄服务器通信闪断/重启告警",
					Server:    address,
					Message:   fmt.Sprintf("服务器监测到通信闪断 (通信中断时长约 %s)，通信已自动恢复。", timeSinceDown.Round(time.Second)),
					Action:    "检测到服务器通信闪断或可能重启，请检查系统日志。",
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendAlert("critical", data) // 重启通常比较重要，建议 severe
				log.Printf("服务器 %s 监测到重启，已发送邮件", address)
			}

			// 重置状态
			tracker.FirstFailureTime = time.Time{}
			tracker.AlertSent = false
		}
	}
}

// 处理端口状态
func handlePortStatus(address, port, key string, portState bool, status *ServerStatus, config Config, portToProcessMap map[string][]string) {
	status.PortMutex.Lock()
	defer status.PortMutex.Unlock()
	// 获取配置窗口，默认 60秒
	windowSeconds := config.PortRestartWindow
	if windowSeconds <= 0 {
		windowSeconds = 180
	}
	window := time.Duration(windowSeconds) * time.Second
	now := time.Now()
	// 获取状态追踪器 (Key 使用端口号)
	tracker, exists := status.PortStates[port]
	if !exists {
		tracker = &StateTracker{}
		status.PortStates[port] = tracker
	}

	if !portState {
		// === 端口不通 (DOWN) ===
		// [新增逻辑] 检查关联进程是否异常
		shouldSuppress := false
		var abnormalProcName string

		if targetProcs, ok := portToProcessMap[port]; ok {
			// 为了安全读取进程状态，需要加 ProcessMutex 锁
			// 安全性说明：当前持有 PortMutex -> 获取 ProcessMutex。
			// 只要代码中没有"持有 ProcessMutex 时去获取 PortMutex"的情况，就不会死锁。
			status.ProcessMutex.Lock()
			for _, procName := range targetProcs {
				if procTracker, procExists := status.ProcessStates[procName]; procExists {
					// 如果关联进程 FirstFailureTime 不为零，说明进程挂了/重启中
					if !procTracker.FirstFailureTime.IsZero() {
						shouldSuppress = true
						abnormalProcName = procName
						break
					}
				}
			}
			status.ProcessMutex.Unlock()
		}

		if shouldSuppress {
			// 如果决定压制，我们依然记录故障时间但不发邮件
			if tracker.FirstFailureTime.IsZero() {
				tracker.FirstFailureTime = now
			}
			log.Printf("🚫 [压制告警] 服务器 %s 端口 %s 异常，但检测到关联进程 [%s] 异常，优先显示进程告警。", address, port, abnormalProcName)
			return // 直接返回，不发送端口告警
		}

		// --- 正常端口不通告警逻辑 ---
		if tracker.FirstFailureTime.IsZero() {
			// 刚发现不通：记录时间，进入观察期
			tracker.FirstFailureTime = now
			log.Printf("服务器 %s 端口 %s 异常失联，进入观察期 (%ds)", address, port, windowSeconds)
		} else {
			// 持续不通
			timeSinceDown := now.Sub(tracker.FirstFailureTime)
			// 超过窗口期且未告警 -> 发送严重告警
			if timeSinceDown > window && !tracker.AlertSent {
				data := EmailTemplateData{
					Subject:   "⚠️⚠️端口失联告警",
					Server:    address,
					Message:   fmt.Sprintf("服务器端口 %s 失联已超过 %d 秒，请检查相关进程！", port, windowSeconds),
					Action:    "请登录服务器检查端口监听状态及进程日志。",
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendAlert("severe", data) // 使用 severe 级别
				tracker.AlertSent = true
				log.Printf("服务器 %s 端口 %s 确认失联，已发送告警", address, port)
			}
		}
	} else {
		// === 端口通 (UP) ===
		if !tracker.FirstFailureTime.IsZero() {
			timeSinceDown := now.Sub(tracker.FirstFailureTime)
			// [新增逻辑] 恢复时检查关联进程，如果是进程重启带动的端口重启，则压制端口恢复/闪断告警
			shouldSuppressRecovery := false
			if targetProcs, ok := portToProcessMap[port]; ok {
				status.ProcessMutex.Lock()
				for _, procName := range targetProcs {
					if procTracker, procExists := status.ProcessStates[procName]; procExists {
						// 进程也还在异常状态（FirstFailureTime非零），说明是一起恢复中
						if !procTracker.FirstFailureTime.IsZero() {
							shouldSuppressRecovery = true
							break
						}
						// 进程虽然现在是好的，但它在"最近一段时间内"刚恢复过
						// 这里的 300秒 建议设置得比您的 portCoolDown (120s) + 检测周期 要大
						if time.Since(procTracker.LastProcessRecoveryTime) < window {
							shouldSuppressRecovery = true
							log.Printf("检测到关联进程 %s 在近期 (%v前) 有过重启，压制端口告警", procName, time.Since(procTracker.LastProcessRecoveryTime))
							break
						}
					}
				}
				status.ProcessMutex.Unlock()
			}

			if tracker.AlertSent {
				// 之前发过失联告警 -> 发送恢复通知
				data := EmailTemplateData{
					Subject:   "✅端口通信恢复",
					Server:    address,
					Message:   fmt.Sprintf("服务器端口 %s 通信已恢复正常。", port),
					Timestamp: now.Format("2006-01-02 15:04:05"),
				}
				sendAlert("recovery", data)
				log.Printf("服务器 %s 端口 %s 通信恢复，发送恢复邮件", address, port)
			} else {
				// 没发过严重告警 -> 判定为闪断/重启
				if shouldSuppressRecovery {
					log.Printf("🚫 [压制告警] 服务器 %s 端口 %s 重启/闪断，因关联进程也在重启周期内，忽略此条。", address, port)
				} else {
					// 没发过告警就恢复了 -> 判定为 端口闪断/进程重启
					data := EmailTemplateData{
						Subject:   "🔄端口重启/闪断告警",
						Server:    address,
						Message:   fmt.Sprintf("服务器端口 %s 发生闪断或重启 (中断时长约 %s)。", port, timeSinceDown.Round(time.Second)),
						Action:    "检测到端口短时间不可用，请关注服务稳定性。",
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					// 建议使用 warning 级别
					sendAlert("severe", data)
					log.Printf("服务器 %s 端口 %s 检测到闪断/重启，发送告警", address, port)
				}
			}

			// 重置状态
			tracker.FirstFailureTime = time.Time{}
			tracker.AlertSent = false
		}
	}
}

// 处理进程状态 (支持重启/消失/恢复逻辑)
func handleProcessStatus(address, key string, processes []ProcessStatus, status *ServerStatus, config Config) {
	status.ProcessMutex.Lock()
	defer status.ProcessMutex.Unlock()

	// 1. 获取配置的窗口时间，默认 60秒
	windowSeconds := config.ProcessRestartWindow
	if windowSeconds <= 0 {
		windowSeconds = 180
	}
	window := time.Duration(windowSeconds) * time.Second
	now := time.Now()

	for _, result := range processes {
		procName := result.ProcessName
		isRunning := result.IsRunning

		// 获取该进程的状态追踪器
		tracker, exists := status.ProcessStates[procName]
		if !exists {
			tracker = &StateTracker{}
			status.ProcessStates[procName] = tracker
		}

		if !isRunning {
			// === 当前状态：进程不存在 (DOWN) ===

			if tracker.FirstFailureTime.IsZero() {
				// A. 刚发现挂了：记录时间，暂不告警 (进入 Pending DOWN)
				tracker.FirstFailureTime = now
				log.Printf("服务器 %s 进程 %s 异常停止，进入观察期 (%ds)", address, procName, windowSeconds)
			} else {
				// B. 已经挂了一段时间了
				timeSinceDown := now.Sub(tracker.FirstFailureTime)

				// 如果超过了窗口期，且还没发过告警 -> 发送 "进程消失" 告警
				if timeSinceDown > window && !tracker.AlertSent {
					data := EmailTemplateData{
						Subject:   "⚠️进程消失告警",
						Server:    address,
						Message:   fmt.Sprintf("服务器进程 %s 已停止超过 %d 秒，确认为故障。", procName, windowSeconds),
						Action:    "请登录服务器检查进程是否存在，或者是否有重启现象！",
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					sendAlert("severe", data)
					tracker.AlertSent = true // 标记为已发送确认故障告警
					log.Printf("服务器 %s 进程 %s 确认消失 (超过观察期)，已发送告警", address, procName)
				}
			}

		} else {
			// === 当前状态：进程运行中 (UP) ===

			if !tracker.FirstFailureTime.IsZero() {
				// [v2.5.0新增] 记录恢复时间
				tracker.LastProcessRecoveryTime = time.Now()
				// 之前有故障记录，现在恢复了 -> 需要判断是重启还是恢复
				timeSinceDown := now.Sub(tracker.FirstFailureTime)

				if tracker.AlertSent {
					// C. 之前已经发过 "消失告警" (Confirmed DOWN -> UP)
					// 这意味着故障时间 > 窗口期，现在是 "恢复"
					data := EmailTemplateData{
						Subject:   "✅进程已启动(恢复)",
						Server:    address,
						Message:   fmt.Sprintf("服务器进程 %s 已重新启动！", procName),
						Action:    "进程已恢复，请观察是否有异常！",
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					sendAlert("recovery", data)
					log.Printf("服务器 %s 进程 %s 已恢复，发送恢复邮件", address, procName)

				} else {
					// D. 还没发过告警就恢复了 (Pending DOWN -> UP)
					// 这意味着故障时间 < 窗口期，判定为 "重启"
					data := EmailTemplateData{
						Subject:   "⚠️🔄进程重启告警",
						Server:    address,
						Message:   fmt.Sprintf("服务器进程 %s 发生重启 (中断时长约 %s)。", procName, timeSinceDown.Round(time.Second)),
						Action:    "检测到进程在短时间内重启，请检查应用日志。",
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					// 这里根据需要可以选择 warning 或 info 级别
					sendAlert("warning", data)
					log.Printf("服务器 %s 进程 %s 检测到重启，发送告警", address, procName)
				}

				// 重置状态，回到正常状态
				tracker.FirstFailureTime = time.Time{} // 重置为 Zero
				tracker.AlertSent = false
			}
			// 如果本来就是正常的 (FirstFailureTime IsZero)，什么都不用做
		}
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
			sendAlert("warning", data)
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
			sendAlert("recovery", data)
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
		sendAlert("warning", data)
		log.Printf("服务器 %s 内存利用率已达到 %.2f%% 超过阈值，请确认告警邮件已发送", address, metrics.MemoryUsage)
		atomic.StoreInt32(&status.MemAlertSent, 1)
	} else if metrics.MemoryUsage < server.MemoryThreshold && memAlertSent {
		data := EmailTemplateData{
			Subject:   "✅内存使用率已降低",
			Server:    address,
			Message:   "服务器内存使用率已降低，恢复正常！",
			Value:     fmt.Sprintf("%.2f%%", metrics.MemoryUsage),
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		}
		sendAlert("recovery", data)
		log.Printf("服务器 %s 内存利用率已降到 %.2f%% ，请确认恢复邮件已发送", address, metrics.MemoryUsage)
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
			sendAlert("critical", data)
			log.Printf("服务器 %s 挂载点 %s 利用率已达到 %.2f%% 超过阈值，请确认告警邮件已发送",
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
			sendAlert("recovery", data)
			log.Printf("服务器 %s 挂载点 %s 利用率已降到 %.2f%% ，请确认恢复邮件已发送",
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
			sendAlert("warning", data)
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
			sendAlert("recovery", data)
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
				sendAlert("warning", data)
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
				sendAlert("recovery", data)
				log.Printf("服务器 %s 目录 %s 文件已存在，请确认恢复邮件已发送",
					address, baseDir)
				(*fileMap)[baseDir] = 0
			}
		}
	}
}

// 处理目标端口状态
func handleTargetPortStatus(address, key string, ports []PortStatus, status *ServerStatus, config Config) {
	status.TargetPortMutex.Lock()
	defer status.TargetPortMutex.Unlock()

	windowSeconds := config.PortRestartWindow
	if windowSeconds <= 0 {
		windowSeconds = 180
	}
	window := time.Duration(windowSeconds) * time.Second
	now := time.Now()

	for _, result := range ports {
		hostName := result.Host
		targetPort := result.Port
		portState := result.Status

		// 获取追踪器 (Key 使用目标主机名)
		tracker, exists := status.TargetPortStates[hostName]
		if !exists {
			tracker = &StateTracker{}
			status.TargetPortStates[hostName] = tracker
		}

		if !portState {
			// === 目标端口不通 ===
			if tracker.FirstFailureTime.IsZero() {
				tracker.FirstFailureTime = now
				log.Printf("服务器 %s 访问目标 %s:%d 失败，进入观察期", address, hostName, targetPort)
			} else {
				timeSinceDown := now.Sub(tracker.FirstFailureTime)
				if timeSinceDown > window && !tracker.AlertSent {
					data := EmailTemplateData{
						Subject:   "⚠️目标服务器端口失联告警",
						Server:    address,
						Message:   fmt.Sprintf("从服务器 %s 到目标 %s:%d 通信失联超过 %d 秒！", address, hostName, targetPort, windowSeconds),
						Action:    "请检查网络链路或目标服务器状态。",
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					sendAlert("severe", data)
					tracker.AlertSent = true
					log.Printf("目标 %s:%d 确认失联，发送告警", hostName, targetPort)
				}
			}
		} else {
			// === 目标端口恢复 ===
			if !tracker.FirstFailureTime.IsZero() {
				timeSinceDown := now.Sub(tracker.FirstFailureTime)
				if tracker.AlertSent {
					data := EmailTemplateData{
						Subject:   "✅目标服务器端口通信恢复",
						Server:    address,
						Message:   fmt.Sprintf("到目标 %s:%d 的通信已恢复。", hostName, targetPort),
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					sendAlert("recovery", data)
				} else {
					data := EmailTemplateData{
						Subject:   "🔄目标端口闪断告警",
						Server:    address,
						Message:   fmt.Sprintf("到目标 %s:%d 发生通信闪断 (时长约 %s)。", hostName, targetPort, timeSinceDown.Round(time.Second)),
						Timestamp: now.Format("2006-01-02 15:04:05"),
					}
					sendAlert("warning", data)
				}
				tracker.FirstFailureTime = time.Time{}
				tracker.AlertSent = false
			}
		}
	}
}

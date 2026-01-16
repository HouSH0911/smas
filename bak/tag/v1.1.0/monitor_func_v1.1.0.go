package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// 监控服务器端口状态（端口通信状态告警）
func monitorServerPorts(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.PortMonitor {
		//log.Println("Port monitoring is disabled in configuration.")
		return
	}
	log.Println("Entering monitorServerPorts function")
	var wg sync.WaitGroup          // 使用 WaitGroup 来等待所有 goroutine 完成
	sem := make(chan struct{}, 15) // 限制并发数为 10
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			wg.Add(1)         // 添加一个 goroutine 到 WaitGroup 中
			sem <- struct{}{} // 占用一个并发槽
			go func(address string, server Server) {
				defer wg.Done()
				defer func() { <-sem }() // 释放一个并发槽
				//address := server.Address
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
			}(address, server)
		}
	}
	// 等待所有 goroutine 完成
	wg.Wait()
	log.Println("left monitorServerPorts function")
}

// 监控服务器通信状态（是否ping通）
func monitorServersState(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.ServerReachMonitor {
		//log.Println("Ping monitoring is disabled in configuration.")
		return
	}
	log.Println("Entering monitorServersState function")
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // 限制并发数为 10
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			wg.Add(1)
			sem <- struct{}{} // 占用一个并发槽
			go func(address string, server Server) {
				defer wg.Done()
				defer func() { <-sem }() // 释放一个并发槽

				//address := server.Address
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
			}(address, server)
		}
	}
	wg.Wait()
	log.Println("left monitorServersState function")
}

// 监控服务器进程状态
func monitorServersProcess(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.ProcessMonitor {
		//log.Println("Process monitoring is disabled in configuration.")
		return
	}
	log.Println("Entering monitorServersProcess function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 15) // 限制并发数为10
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			wg.Add(1)
			sem <- struct{}{}
			go func(address string, server Server) {
				defer wg.Done()
				defer func() { <-sem }()
				//address := server.Address
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
			}(address, server)
		}
	}
	wg.Wait()
	log.Println("left monitorServersProcess function") // 调试日志
}

// 监控服务器的状态，包括CPU、内存、磁盘利用率
func monitorResources(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.Resource {
		//log.Println("Resource monitoring is disabled in configuration.")
		return
	}

	log.Println("Entering monitorResource function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // 限制并发数为10
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			wg.Add(1)
			sem <- struct{}{}
			go func(address string, server Server) {
				defer wg.Done()
				defer func() { <-sem }() // 释放一个并发槽
				//address := server.Address
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
					// 检查该挂载点是否在排除列表中
					if isExcludedMountPoint(mountpoint, server.ExcludeMountPoints) {
						continue // 跳过被排除的挂载点
					}
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
			}(address, server)
		}

	}
	wg.Wait()                                    // 等待所有 goroutine 完成
	log.Println("left monitorResource function") // 调试日志
}

// 监测服务器目录和文件模块
func monitorDirectory(config Config, statuses map[string]*ServerStatus) {
	if !config.Monitor.DirFileMonitor {
		//log.Println("Directory monitoring is disabled in configuration.")
		return
	}

	log.Println("Entering monitorDirectory function") // 调试日志
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // 限制并发数为10
	for _, server := range config.Servers {
		for _, address := range server.Addresses {
			wg.Add(1)
			sem <- struct{}{}
			go func(address string, server Server) {
				defer wg.Done()
				defer func() { <-sem }() // 释放一个并发槽
				//address := server.Address
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
			}(address, server)
		}
	}
	wg.Wait()                                     // 等待所有 goroutine 完成
	log.Println("left monitorDirectory function") // 调试日志
}

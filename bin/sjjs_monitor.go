package main

// 新建 stream_report.go

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// 启动话单统计报告调度器 (在 main 函数中调用)
func startStreamReportScheduler() {
	if !config.StreamReport.Enabled {
		return
	}
	log.Printf("话单流统计报告调度已启动，设定时间: %s", config.StreamReport.ReportTime)

	for {
		now := time.Now()
		targetTime := parseReportTime(config.StreamReport.ReportTime) // 复用之前的解析函数

		if now.After(targetTime) {
			targetTime = targetTime.Add(24 * time.Hour)
		}

		duration := targetTime.Sub(now)
		log.Printf("距离下一次话单统计报告还有: %v", duration)

		// 等待到指定时间
		time.Sleep(duration)

		// 执行统计
		runStreamStatsCollection()

		// 防止1秒内重复执行，稍微休眠
		time.Sleep(2 * time.Second)
	}
}

// 执行话单统计收集
func runStreamStatsCollection() {
	log.Println("开始收集全网话单统计数据...")

	// 结果容器
	type ServerStreamResult struct {
		Address string
		Stats   []StreamStat
		Error   error
	}

	var wg sync.WaitGroup
	resultChan := make(chan ServerStreamResult, 500) // 缓冲设大一点

	// 遍历所有配置的服务器
	count := 0
	for _, server := range config.Servers {
		for _, addr := range server.Addresses {
			count++
			wg.Add(1)
			go func(ip string) {
				defer wg.Done()

				// 1. 发起 HTTP 请求 (复用 getMetricsData)
				// 注意：这里需要确保 getMetricsData 能返回 byte 数组
				resp, err := getCachedCheckData(ip)
				if err != nil {
					resultChan <- ServerStreamResult{Address: ip, Error: err}
					return
				}

				// 2. 解析数据
				if resp == nil {
					resultChan <- ServerStreamResult{Address: ip, Error: fmt.Errorf("返回数据为空")}
					return
				}

				// 3. 返回结果 (即使 StreamStats 为空也返回，用于判断是否有数据)
				resultChan <- ServerStreamResult{Address: ip, Stats: resp.StreamStats}
			}(addr)
		}
	}

	wg.Wait()
	close(resultChan)

	// --- 聚合数据 ---

	var totalFilesAll int64 = 0
	var totalSizeAll int64 = 0

	// 统计成功的服务器数量
	successCount := 0
	// 记录未统计到的服务器 (Error 或 Stats 为空)
	var missingServers []string

	for res := range resultChan {
		if res.Error != nil {
			missingServers = append(missingServers, fmt.Sprintf("%s (连接/解析失败)", res.Address))
			continue
		}

		if len(res.Stats) == 0 {
			// 如果返回了JSON但 streamStats 字段是空的，也算作未统计到数据(视业务情况而定，这里假设必须有数据)
			missingServers = append(missingServers, fmt.Sprintf("%s (无话单数据)", res.Address))
			continue
		}

		successCount++
		// 累加该服务器上所有流的数据
		for _, stat := range res.Stats {
			totalFilesAll += stat.TotalFiles
			totalSizeAll += stat.TotalSize
		}
	}

	// --- 生成报告并发送 ---

	sendStreamReportAlert(totalFilesAll, totalSizeAll, successCount, count, missingServers)
}

// 发送告警/报告
func sendStreamReportAlert(totalFiles, totalSize int64, successCount, totalServers int, missingServers []string) {
	// 格式化大小 (B -> GB/TB)
	humanSize := formatBytes(totalSize)

	// 构建消息内容
	subject := fmt.Sprintf("📊 全网话单上传统计日报 [%s]", time.Now().Format("2006-01-02"))

	msgBuilder := ""
	msgBuilder += fmt.Sprintf("统计时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	msgBuilder += fmt.Sprintf("监控节点: 共 %d 台 (成功 %d 台，失败 %d 台)\n", totalServers, successCount, len(missingServers))
	msgBuilder += "--------------------------------\n"
	msgBuilder += fmt.Sprintf("📂 总文件数量: %d 个\n", totalFiles)
	msgBuilder += fmt.Sprintf("💾 总文件大小: %s\n", humanSize)
	msgBuilder += "--------------------------------\n"

	if len(missingServers) > 0 {
		msgBuilder += "⚠️ 以下节点未统计到数据:\n"
		for _, s := range missingServers {
			msgBuilder += fmt.Sprintf("- %s\n", s)
		}
	} else {
		msgBuilder += "✅ 所有节点均正常上报数据。\n"
	}

	// 构造 EmailTemplateData (复用现有的模板结构)
	data := EmailTemplateData{
		Subject:   subject,
		Message:   msgBuilder, // 这里如果是HTML邮件，可能需要换行符替换为<br>
		Server:    "Aggregation-Node",
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}

	// 发送 (使用 info 或 warning 级别)
	log.Println(msgBuilder)
	sendAlert("info", data)
}

// 辅助函数：字节转人类可读格式
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

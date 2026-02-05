package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// --- 调度逻辑 ---

// 启动话单统计报告调度器 (轮询模式)
func startStreamReportScheduler() {
	log.Println("🚀 话单流统计调度器已就绪...")

	// 记录上一次执行的日期
	var lastRunDate string

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if !config.StreamReport.Enabled {
			continue
		}

		now := time.Now()
		targetTime := parseReportTime(config.StreamReport.ReportTime)

		// 匹配小时和分钟
		if now.Hour() == targetTime.Hour() && now.Minute() == targetTime.Minute() {
			today := now.Format("2006-01-02")

			if lastRunDate != today {
				log.Printf("⏰ 触发话单统计时间点，开始执行...")
				go runStreamStatsCollection()
				lastRunDate = today
			}
		}
	}
}

// --- 收集逻辑 ---

func runStreamStatsCollection() {
	log.Println("开始收集全网话单统计数据...")

	type ServerStreamResult struct {
		Address      string
		Stats        []StreamStat
		Error        error
		NoStreamData bool // 标记返回成功但 streamStats 为 null 或空
	}

	// 1. 筛选 XDR 服务器
	var targetAddresses []string
	for _, server := range config.Servers {
		if server.ServerType == "XDR" {
			targetAddresses = append(targetAddresses, server.Addresses...)
		}
	}

	if len(targetAddresses) == 0 {
		log.Println("⚠️ 未找到 ServerType='XDR' 的服务器，统计取消。")
		return
	}

	// 2. 并发采集
	var wg sync.WaitGroup
	resultChan := make(chan ServerStreamResult, len(targetAddresses))

	for _, addr := range targetAddresses {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			resp, err := getCachedCheckData(ip)
			if err != nil {
				resultChan <- ServerStreamResult{Address: ip, Error: err}
				return
			}
			if resp == nil {
				resultChan <- ServerStreamResult{Address: ip, Error: fmt.Errorf("返回数据为空")}
				return
			}
			// 检查 streamStats 是否为 null 或空
			if len(resp.StreamStats) == 0 {
				resultChan <- ServerStreamResult{Address: ip, NoStreamData: true}
				return
			}
			resultChan <- ServerStreamResult{Address: ip, Stats: resp.StreamStats}
		}(addr)
	}

	wg.Wait()
	close(resultChan)

	// 3. 聚合数据
	aggregatedMap := make(map[string]*AggregatedStream)
	for _, targetName := range config.StreamReport.TargetStreams {
		aggregatedMap[targetName] = &AggregatedStream{
			StreamName: targetName,
			StatDate:   time.Now().Format("20060102"),
			TotalFiles: 0,
			TotalSize:  0,
		}
	}

	successCount := 0
	var missingServers []string
	var noDataServers []string

	for res := range resultChan {
		if res.Error != nil {
			missingServers = append(missingServers, fmt.Sprintf("%s (%v)", res.Address, res.Error))
			continue
		}
		if res.NoStreamData {
			noDataServers = append(noDataServers, res.Address)
			continue
		}
		successCount++

		for _, stat := range res.Stats {
			if targetStat, exists := aggregatedMap[stat.StreamName]; exists {
				targetStat.TotalFiles += stat.TotalFiles
				targetStat.TotalSize += stat.TotalSize
				if stat.StatDate != "" {
					targetStat.StatDate = stat.StatDate
				}
			}
		}
	}

	// 4. 发送报告
	sendStreamReport(aggregatedMap, successCount, len(targetAddresses), missingServers, noDataServers)
}

// --- 发送逻辑 (参考 report_summary) ---

func sendStreamReport(aggMap map[string]*AggregatedStream, successCount, totalServers int, missingServers, noDataServers []string) {
	var sortedStats []*AggregatedStream
	for _, v := range aggMap {
		sortedStats = append(sortedStats, v)
	}
	sort.Slice(sortedStats, func(i, j int) bool {
		return sortedStats[i].StreamName < sortedStats[j].StreamName
	})

	currentTime := time.Now().Format("2006-01-02 15:04:05")
	reportTitle := fmt.Sprintf("📊 全网话单上传统计日报 [%s]", time.Now().Format("2006-01-02"))

	// ===========================
	// 1. 处理企业微信 (Markdown)
	// ===========================
	if config.AlertMethods.WechatWork {
		mdBuilder := strings.Builder{}

		mdBuilder.WriteString(fmt.Sprintf("**统计时间**: %s\n", currentTime))
		mdBuilder.WriteString(fmt.Sprintf("**监控节点**: 共 %d 台 (正常：%d 台，未采集：%d 台)\n", totalServers, successCount, len(missingServers)+len(noDataServers)))
		mdBuilder.WriteString("--------------------------------\n")

		for _, stat := range sortedStats {
			humanSize := formatBytes(stat.TotalSize)
			mdBuilder.WriteString(fmt.Sprintf("> **%s**: %d 个 / %s\n", stat.StreamName, stat.TotalFiles, humanSize))
		}

		if len(noDataServers) > 0 {
			mdBuilder.WriteString("\n⚠️ **未统计到数据的节点**:\n")
			for _, s := range noDataServers {
				mdBuilder.WriteString(fmt.Sprintf("• <font color=\"warning\">%s</font>\n", s))
			}
		}

		if len(missingServers) > 0 {
			mdBuilder.WriteString("\n❌ **采集失败的节点**:\n")
			for _, s := range missingServers {
				mdBuilder.WriteString(fmt.Sprintf("• <font color=\"warning\">%s</font>\n", s))
			}
		}

		if len(missingServers) == 0 && len(noDataServers) == 0 {
			mdBuilder.WriteString("\n✅ <font color=\"info\">所有节点正常上报</font>")
		}

		data := EmailTemplateData{
			Subject:   reportTitle,
			Message:   mdBuilder.String(),
			Server:    "所有XDR服务器",
			Timestamp: currentTime,
		}
		sendWechatWorkAlert(config.WechatWork, "info", data)
	}

	// ===========================
	// 2. 处理邮件 (HTML 表格)
	// ===========================
	if config.AlertMethods.Email {
		tableStyle := `border-collapse: collapse; width: 100%; max-width: 800px; font-family: Arial, sans-serif;`
		thStyle := `border: 1px solid #ddd; padding: 8px; background-color: #f2f2f2; text-align: left;`
		tdStyle := `border: 1px solid #ddd; padding: 8px;`

		html := strings.Builder{}
		html.WriteString("<html><body>")
		html.WriteString(fmt.Sprintf("<h3>%s</h3>", reportTitle))
		html.WriteString(fmt.Sprintf("<p><strong>统计时间:</strong> %s<br>", currentTime))
		html.WriteString(fmt.Sprintf("<strong>监控节点:</strong> 共 %d 台 (成功 %d, 失败 %d)</p>", totalServers, successCount, len(missingServers)+len(noDataServers)))

		html.WriteString(fmt.Sprintf("<table style='%s'>", tableStyle))
		html.WriteString(fmt.Sprintf("<thead><tr><th style='%s'>数据流</th><th style='%s'>日期</th><th style='%s'>总文件数</th><th style='%s'>总大小</th></tr></thead><tbody>", thStyle, thStyle, thStyle, thStyle))

		for _, stat := range sortedStats {
			humanSize := formatBytes(stat.TotalSize)
			html.WriteString("<tr>")
			html.WriteString(fmt.Sprintf("<td style='%s'><strong>%s</strong></td>", tdStyle, stat.StreamName))
			html.WriteString(fmt.Sprintf("<td style='%s'>%s</td>", tdStyle, stat.StatDate))
			html.WriteString(fmt.Sprintf("<td style='%s'>%d</td>", tdStyle, stat.TotalFiles))
			html.WriteString(fmt.Sprintf("<td style='%s'>%s</td>", tdStyle, humanSize))
			html.WriteString("</tr>")
		}
		html.WriteString("</tbody></table>")

		if len(noDataServers) > 0 {
			html.WriteString("<br><div style='background-color: #fffbe6; border:1px solid #ffe58f; padding:10px;'>")
			html.WriteString("<strong>⚠️ 以下节点未统计到数据 (请检查agent程序配置或importer程序):</strong><br>")
			for _, s := range noDataServers {
				html.WriteString(fmt.Sprintf("%s<br>", s))
			}
			html.WriteString("</div>")
		}

		if len(missingServers) > 0 {
			html.WriteString("<br><div style='background-color: #fff3f3; border:1px solid #ffccc7; padding:10px;'>")
			html.WriteString("<strong>❌ 以下节点采集失败:</strong><br>")
			for _, s := range missingServers {
				html.WriteString(fmt.Sprintf("%s<br>", s))
			}
			html.WriteString("</div>")
		}

		if len(missingServers) == 0 && len(noDataServers) == 0 {
			html.WriteString("<br><p style='color:green'>✅ 所有节点均正常。</p>")
		}
		html.WriteString("</body></html>")

		go sendRawHtmlEmail(config.Email, reportTitle, html.String())
	}
}

// 辅助函数
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

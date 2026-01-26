package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/smtp"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const historyFileName = "alert_history.json"

// 保存告警历史到文件
func saveAlertHistory() {
	// 获取项目根目录 (复用 main.go 中的逻辑，或者这里简单处理，假设在 bin 同级或 config 同级)
	// 这里为了简单，建议直接保存到 log 目录或者 config 目录，这里假设和 config.json 同级
	historyPath := filepath.Join(filepath.Dir(configPath), historyFileName)

	data, err := json.MarshalIndent(alertHistory, "", "  ")
	if err != nil {
		log.Printf("序列化告警历史失败: %v", err)
		return
	}

	err = os.WriteFile(historyPath, data, 0644)
	if err != nil {
		log.Printf("保存告警历史文件失败: %v", err)
	}
}

// 从文件加载告警历史
func LoadAlertHistory() { // 首字母大写供 main 调用
	historyPath := filepath.Join(filepath.Dir(configPath), historyFileName)

	// 如果文件不存在，直接返回
	if _, err := os.Stat(historyPath); os.IsNotExist(err) {
		return
	}

	data, err := os.ReadFile(historyPath)
	if err != nil {
		log.Printf("读取告警历史文件失败: %v", err)
		return
	}

	alertHistoryMutex.Lock()
	defer alertHistoryMutex.Unlock()

	var loadedHistory []AlertRecord
	if err := json.Unmarshal(data, &loadedHistory); err != nil {
		log.Printf("解析告警历史文件失败: %v", err)
		return
	}

	alertHistory = loadedHistory
	log.Printf("成功从本地缓存加载了 %d 条历史告警记录", len(alertHistory))
}

// recordAlert 记录告警到历史
func recordAlert(alertLevel string, data EmailTemplateData) {
	alertHistoryMutex.Lock()
	defer alertHistoryMutex.Unlock()

	// 提取类型从消息中
	alertType := extractAlertType(data.Message)

	record := AlertRecord{
		Time:       data.Timestamp,
		Server:     data.Server,
		AlertLevel: alertLevel,
		Type:       alertType,
		Message:    data.Message,
		Value:      data.Value,
		Threshold:  data.Threshold,
		Action:     data.Action,
		Status:     "active",
	}

	alertHistory = append(alertHistory, record)

	// 限制历史记录数量，保留最近1000条
	if len(alertHistory) > 1000 {
		alertHistory = alertHistory[len(alertHistory)-1000:]
	}
}

// extractAlertType 从消息中提取告警类型
func extractAlertType(message string) string {
	lowerMsg := strings.ToLower(message)
	switch {
	case strings.Contains(lowerMsg, "cpu"):
		return "CPU"
	case strings.Contains(lowerMsg, "内存"):
		return "Memory"
	case strings.Contains(lowerMsg, "磁盘"):
		return "Disk"
	case strings.Contains(lowerMsg, "端口"):
		return "Port"
	case strings.Contains(lowerMsg, "进程"):
		return "Process"
	case strings.Contains(lowerMsg, "ping"):
		return "Ping"
	case strings.Contains(lowerMsg, "目录"):
		return "Dir"
	case strings.Contains(lowerMsg, "文件"):
		return "File"
	default:
		return "Other"
	}
}

// *** 新增：报告调度器 ***
func startReportScheduler() {
	log.Printf("汇总报告调度器已启动，计划时间: %s, 频率: %s", config.SummaryReport.ReportTime, config.SummaryReport.ReportType)

	for {
		now := time.Now()
		// 解析目标时间，例如 "08:00"
		parts := strings.Split(config.SummaryReport.ReportTime, ":")
		if len(parts) != 2 {
			log.Println("配置错误: reportTime 格式应为 HH:MM")
			return
		}
		targetH, _ := strconv.Atoi(parts[0])
		targetM, _ := strconv.Atoi(parts[1])

		// 计算下一次运行时间
		nextRun := time.Date(now.Year(), now.Month(), now.Day(), targetH, targetM, 0, 0, now.Location())

		// 如果今天的已经过了，就设为明天
		if nextRun.Before(now) {
			nextRun = nextRun.Add(24 * time.Hour)
		}

		// 如果是周报，且明天不是周一（假设周一发），则往后推
		if config.SummaryReport.ReportType == "weekly" {
			// 这里简单逻辑：一直加天数直到是周一
			// (注意：这里如果是周一当天已经过了时间，上面的 .Add(24h) 已经变成了周二，逻辑需要严谨)
			// 简单做法：每天醒来检查是不是周一，不是就不发
		}

		duration := nextRun.Sub(now)
		log.Printf("下一次汇总报告将在 %v 后发送", duration)

		// 等待到指定时间
		time.Sleep(duration)

		// 醒来后执行发送
		// 再次检查周报逻辑 (如果是daily直接发，如果是weekly且今天是周一才发)
		shouldSend := true
		if config.SummaryReport.ReportType == "weekly" && time.Now().Weekday() != time.Monday {
			shouldSend = false
		}

		if shouldSend {
			sendSummaryReport()
		}

		//防止并在极短时间内重复执行，休眠一小会儿
		time.Sleep(time.Minute)
	}
}

// 生成统计信息
func generateSummaryStats(records []AlertRecord) SummaryStats {
	stats := SummaryStats{
		IPStats:      make(map[string]IPStat),
		LevelStats:   make(map[string]int),
		TypeStats:    make(map[string]int),
		MessageStats: make(map[string]int),
		TimeStats:    make(map[string]int),
	}

	stats.TotalAlerts = len(records)

	for _, record := range records {
		// 按IP统计
		ipStat, exists := stats.IPStats[record.Server]
		if !exists {
			ipStat = IPStat{
				LevelBreakdown: make(map[string]int),
				TypeBreakdown:  make(map[string]int),
			}
		}
		ipStat.Total++
		ipStat.LevelBreakdown[record.AlertLevel]++
		ipStat.TypeBreakdown[record.Type]++
		stats.IPStats[record.Server] = ipStat

		// 按级别统计
		stats.LevelStats[record.AlertLevel]++

		// 按类型统计
		stats.TypeStats[record.Type]++

		// 按消息内容统计（简化消息，提取关键词）
		simplifiedMsg := simplifyMessage(record.Message)
		stats.MessageStats[simplifiedMsg]++

		// 按时间段统计（小时）
		hour := extractHourFromTimestamp(record.Time)
		stats.TimeStats[hour]++
	}

	return stats
}

// 简化消息内容，提取关键词
func simplifyMessage(message string) string {
	// 定义关键词映射
	keywords := map[string]string{
		"CPU":  "CPU",
		"内存":   "内存",
		"磁盘":   "磁盘",
		"端口":   "端口",
		"进程":   "进程",
		"通信":   "通信",
		"重启":   "重启",
		"恢复":   "恢复",
		"失联":   "失联",
		"超过阈值": "超阈值",
		"使用率":  "使用率",
	}

	// 查找包含的关键词
	for keyword, simplified := range keywords {
		if strings.Contains(message, keyword) {
			return simplified
		}
	}

	// 如果没有匹配的关键词，截取前20个字符
	if len(message) > 20 {
		return message[:20] + "..."
	}
	return message
}

// 从时间戳提取小时
func extractHourFromTimestamp(timestamp string) string {
	if len(timestamp) >= 13 { // 确保有足够长度提取小时
		return timestamp[11:13] + ":00" // 格式如 "14:00"
	}
	return "未知时间"
}

// buildEmailWithStats 构建带统计的邮件内容
func buildEmailWithStats(title, nowStr string, records []AlertRecord, stats SummaryStats) string {
	var htmlContent strings.Builder

	// 标题和基本信息
	htmlContent.WriteString(fmt.Sprintf("<h2>%s</h2>", title))
	htmlContent.WriteString(fmt.Sprintf("<p style='color:gray; font-size:12px;'>统计时间: %s | 总告警数: %d</p>", nowStr, len(records)))

	// ==================== 统计摘要表格 ====================
	htmlContent.WriteString("<h3>统计摘要</h3>")
	htmlContent.WriteString("<table border='1' cellspacing='0' cellpadding='8' style='border-collapse: collapse; width: 100%; margin-bottom: 20px; background-color: #f9f9f9;'>")

	// 1. 按告警级别统计
	htmlContent.WriteString("<tr><td colspan='4' style='background-color: #e6f7ff; font-weight: bold;'>按告警级别统计</td></tr>")
	htmlContent.WriteString("<tr style='background-color: #f0f0f0;'><th>级别</th><th>数量</th><th>占比</th><th>趋势</th></tr>")

	// 按级别排序：critical, severe, warning, recovery
	levelOrder := []string{"critical", "severe", "warning", "recovery"}
	for _, level := range levelOrder {
		if count, exists := stats.LevelStats[level]; exists {
			percentage := float64(count) / float64(stats.TotalAlerts) * 100
			trendIcon := getTrendIcon(level)
			color := getLevelColor(level)

			htmlContent.WriteString(fmt.Sprintf("<tr><td style='color: %s; font-weight: bold;'>%s %s</td><td>%d</td><td>%.1f%%</td><td>%s</td></tr>", color, trendIcon, level, count, percentage, getTrendIndicator(level, count)))
		}
	}

	// 2. 按告警类型统计
	htmlContent.WriteString("<tr><td colspan='4' style='background-color: #fff7e6; font-weight: bold;'>按告警类型统计</td></tr>")
	htmlContent.WriteString("<tr style='background-color: #f0f0f0;'><th>类型</th><th>数量</th><th>占比</th><th>主要问题</th></tr>")

	// 对类型按数量排序
	var typeKeys []string
	for typ := range stats.TypeStats {
		typeKeys = append(typeKeys, typ)
	}
	sort.Strings(typeKeys)

	for _, typ := range typeKeys {
		count := stats.TypeStats[typ]
		percentage := float64(count) / float64(stats.TotalAlerts) * 100
		mainIssue := getMainIssueForType(typ, stats.MessageStats)

		htmlContent.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%d</td><td>%.1f%%</td><td>%s</td></tr>", typ, count, percentage, mainIssue))
	}
	htmlContent.WriteString("</table>")

	// ==================== 服务器告警排名 ====================
	htmlContent.WriteString("<h3>🏆 服务器告警排名</h3>")
	htmlContent.WriteString("<table border='1' cellspacing='0' cellpadding='8' style='border-collapse: collapse; width: 100%; margin-bottom: 20px;'>")
	htmlContent.WriteString("<tr style='background-color: #f0f0f0;'><th>排名</th><th>服务器IP</th><th>总告警数</th><th>严重告警</th><th>主要问题</th></tr>")

	// 对服务器按告警数排序
	rankedIPs := rankIPsByAlerts(stats.IPStats)
	for i, ip := range rankedIPs {
		if i >= 10 { // 只显示前10名
			break
		}
		ipStat := stats.IPStats[ip]
		mainProblem := getMainProblemForIP(ipStat)

		rankIcon := getRankIcon(i + 1)
		htmlContent.WriteString(fmt.Sprintf(
			"<tr><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>", rankIcon, ip, ipStat.Total, ipStat.LevelBreakdown["critical"]+ipStat.LevelBreakdown["severe"], mainProblem))
	}
	htmlContent.WriteString("</table>")

	// ==================== 时间段分布 ====================
	htmlContent.WriteString("<h3>⏰ 告警时间段分布</h3>")
	htmlContent.WriteString("<table border='1' cellspacing='0' cellpadding='8' style='border-collapse: collapse; width: 100%; margin-bottom: 20px;'>")
	htmlContent.WriteString("<tr style='background-color: #f0f0f0;'><th>时间段</th><th>告警数量</th><th>分布图</th></tr>")

	// 按小时排序显示
	hours := getSortedHours(stats.TimeStats)
	for _, hour := range hours {
		count := stats.TimeStats[hour]
		barLength := (count * 100) / stats.TotalAlerts
		if barLength == 0 && count > 0 {
			barLength = 1
		}

		htmlContent.WriteString(fmt.Sprintf(
			"<tr><td>%s时</td><td>%d</td><td><div style='background-color: #1890ff; width: %d%%; height: 20px;'></div></td></tr>", hour, count, barLength))
	}
	htmlContent.WriteString("</table>")

	// ==================== 新增：详细告警记录表格 ====================
	if len(records) > 0 {
		htmlContent.WriteString("<h3>📋 详细告警记录</h3>")
		htmlContent.WriteString("<table border='1' cellspacing='0' cellpadding='8' style='border-collapse: collapse; width: 100%; font-size: 13px; font-family: Arial, sans-serif; margin-bottom: 20px;'>")

		// 表头
		htmlContent.WriteString("<tr style='background-color: #f2f2f2; text-align: left;'>")
		htmlContent.WriteString("<th style='padding: 8px;'>时间</th>")
		htmlContent.WriteString("<th style='padding: 8px;'>服务器</th>")
		htmlContent.WriteString("<th style='padding: 8px;'>级别</th>")
		htmlContent.WriteString("<th style='padding: 8px;'>类型</th>")
		htmlContent.WriteString("<th style='padding: 8px;'>内容</th>")
		htmlContent.WriteString("</tr>")

		// 遍历记录填充表格
		for _, r := range records {
			// 根据级别设置颜色样式
			rowStyle := ""
			statusColor := "black"
			if r.AlertLevel == "critical" || r.AlertLevel == "severe" {
				statusColor = "#d9534f"                 // 红色
				rowStyle = "background-color: #fff5f5;" // 浅红背景
			} else if r.AlertLevel == "recovery" {
				statusColor = "#5cb85c"                 // 绿色
				rowStyle = "background-color: #f5fff5;" // 浅绿背景
			} else if r.AlertLevel == "warning" {
				statusColor = "#f0ad4e"                 // 橙色
				rowStyle = "background-color: #fffaf0;" // 浅橙背景
			}

			htmlContent.WriteString(fmt.Sprintf("<tr style='%s'>", rowStyle))
			htmlContent.WriteString(fmt.Sprintf("<td style='padding: 8px;'>%s</td>", r.Time))
			htmlContent.WriteString(fmt.Sprintf("<td style='padding: 8px;'>%s</td>", r.Server))
			htmlContent.WriteString(fmt.Sprintf("<td style='padding: 8px; color:%s; font-weight:bold;'>%s</td>", statusColor, r.AlertLevel))
			htmlContent.WriteString(fmt.Sprintf("<td style='padding: 8px;'>%s</td>", r.Type))
			htmlContent.WriteString(fmt.Sprintf("<td style='padding: 8px;'>%s</td>", r.Message))
			htmlContent.WriteString("</tr>")
		}
		htmlContent.WriteString("</table>")
	}

	// ==================== 邮件页脚（保持不变） ====================
	htmlContent.WriteString("<p style='font-size:12px; color:gray;'>本邮件由监控系统自动生成，请勿回复。</p>")

	return htmlContent.String()
}

// buildWechatSummaryWithStats 构建带统计的企业微信消息
func buildWechatSummaryWithStats(title, nowStr string, records []AlertRecord, stats SummaryStats) string {
	var mdContent strings.Builder

	mdContent.WriteString(fmt.Sprintf("# 📊 %s\n\n", title))
	mdContent.WriteString(fmt.Sprintf("> 生成时间: %s\n", nowStr))
	mdContent.WriteString(fmt.Sprintf("> 总告警数: %d 条\n\n", len(records)))

	if len(records) == 0 {
		mdContent.WriteString("🎉 过去周期内无告警，一切正常！")
		return mdContent.String()
	}

	// ==================== 统计摘要 ====================
	mdContent.WriteString("## 统计摘要\n\n")

	// 1. 按级别统计
	mdContent.WriteString("### 1、告警级别统计\n")
	levelOrder := []string{"critical", "severe", "warning", "recovery"}
	for _, level := range levelOrder {
		if count, exists := stats.LevelStats[level]; exists {
			percentage := float64(count) / float64(stats.TotalAlerts) * 100
			emoji := getTrendIcon(level)
			mdContent.WriteString(fmt.Sprintf("- %s **%s**: %d 条 (%.1f%%)\n", emoji, level, count, percentage))
		}
	}

	// 2. 按类型统计
	mdContent.WriteString("\n### 2、告警类型统计\n")
	var typeKeys []string
	for typ := range stats.TypeStats {
		typeKeys = append(typeKeys, typ)
	}
	sort.Strings(typeKeys)

	for _, typ := range typeKeys {
		count := stats.TypeStats[typ]
		percentage := float64(count) / float64(stats.TotalAlerts) * 100
		mdContent.WriteString(fmt.Sprintf("- **%s**: %d 条 (%.1f%%)\n", typ, count, percentage))
	}

	// 3. 服务器排名（前5）
	mdContent.WriteString("\n### 3、服务器告警排名（TOP5）\n")
	rankedIPs := rankIPsByAlerts(stats.IPStats)
	for i, ip := range rankedIPs {
		if i >= 5 {
			break
		}
		ipStat := stats.IPStats[ip]
		mdContent.WriteString(fmt.Sprintf("%d. **%s**: %d 条\n", i+1, ip, ipStat.Total))
	}

	// 4. 时间段分布
	mdContent.WriteString("\n### 4、高峰时间段\n")
	hours := getSortedHours(stats.TimeStats)
	for _, hour := range hours {
		count := stats.TimeStats[hour]
		if count > 0 {
			barLength := (count * 10) / stats.TotalAlerts
			if barLength == 0 {
				barLength = 1
			}
			bar := strings.Repeat("█", barLength)
			mdContent.WriteString(fmt.Sprintf("- %s时: %s %d条\n", hour, bar, count))
		}
	}

	return mdContent.String()
}

// *** 新增：生成并发送汇总报告 ***
// sendSummaryReport 生成并发送汇总报告
func sendSummaryReport() {
	alertHistoryMutex.Lock()
	// 1. 取出数据并清空历史
	records := alertHistory
	// 重置切片，准备记录下一周期的
	alertHistory = []AlertRecord{}
	alertHistoryMutex.Unlock()

	count := len(records)
	if count == 0 {
		log.Println("过去周期内无告警，跳过汇总报告")
		return
	}

	log.Printf("开始发送汇总报告，共 %d 条记录", count)

	// 生成统计信息
	stats := generateSummaryStats(records)
	title := fmt.Sprintf("%s (共 %d 条告警)", config.SummaryReport.Title, count)
	nowStr := time.Now().Format("2006-01-02 15:04:05")

	// ==========================================
	// A. 发送邮件 (HTML 表格格式)
	// ==========================================
	if config.EnableEmail {
		// 构建HTML内容
		htmlContent := buildEmailWithStats(title, nowStr, records, stats)

		go func() {
			err := sendRawHtmlEmail(config.Email, title, htmlContent)
			if err != nil {
				log.Printf("发送汇总邮件失败: %v", err)
			} else {
				log.Printf("汇总邮件发送成功")
			}
		}()
	}

	// ==========================================
	// B. 发送企业微信 (Markdown摘要 + CSV文件)
	// ==========================================
	// if config.AlertMethods.WechatWork && config.WechatWork.Enabled {
	if config.WechatWork.Enabled {
		// 1. 构建Markdown摘要消息
		mdContent := buildWechatSummaryWithStats(title, nowStr, records, stats)

		// 添加文件提示
		if len(records) > 5 {
			mdContent += "\n\n---\n📎 **文件附件**\n"
			mdContent += fmt.Sprintf("- 已生成详细CSV文件，包含 %d 条完整记录\n", len(records))
			mdContent += "- 文件已自动发送，参考下方附件\n"
		}

		// 选择目标URL
		var targetUrl string
		if config.WechatWork.ProxyEnabled && config.WechatWork.ProxyUrl != "" {
			key := extractKeyFromWebhookUrl(config.WechatWork.WebhookUrl)
			targetUrl = fmt.Sprintf("%s/webhook?key=%s", config.WechatWork.ProxyUrl, key)
		} else {
			targetUrl = config.WechatWork.WebhookUrl
		}

		// 构建Markdown消息
		msg := WechatWorkMessage{
			MsgType: "markdown",
			Markdown: struct {
				Content string `json:"content"`
			}{Content: mdContent},
		}

		// 先发送Markdown摘要
		go sendWechatWorkRequest(targetUrl, msg)

		// 2. 然后发送CSV文件（如果记录数较多）
		if len(records) > 0 {
			go func() {
				// 等待2秒，确保文本消息先到达
				time.Sleep(2 * time.Second)

				// 生成CSV文件
				csvContent := generateCSVContent(records)
				filename := fmt.Sprintf("监控汇总_%s.csv", time.Now().Format("20060102_150405"))

				log.Printf("开始发送CSV文件附件: %s (%d 字节)", filename, len(csvContent))

				// 发送文件
				sendWechatWorkFile(targetUrl, filename, []byte(csvContent))
			}()
		}

		log.Printf("企业微信汇总消息发送成功")
	}
}

// generateCSVContent 生成CSV格式的详细记录
func generateCSVContent(records []AlertRecord) string {
	var csvBuilder strings.Builder

	// CSV头部（带BOM防止中文乱码）
	csvBuilder.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	csvBuilder.WriteString("时间,服务器,告警级别,告警类型,告警内容,当前值,阈值,建议操作\n")

	for _, record := range records {
		// 处理特殊字符，防止破坏CSV格式
		message := strings.ReplaceAll(record.Message, "\"", "\"\"")
		message = strings.ReplaceAll(message, ",", "，")
		message = strings.ReplaceAll(message, "\n", " ")

		value := strings.ReplaceAll(record.Value, ",", "，")
		threshold := strings.ReplaceAll(record.Threshold, ",", "，")
		action := strings.ReplaceAll(record.Action, ",", "，")

		csvBuilder.WriteString(fmt.Sprintf("\"%s\",\"%s\",\"%s\",\"%s\",\"%s\",\"%s\",\"%s\",\"%s\"\n",
			record.Time,
			record.Server,
			record.AlertLevel,
			record.Type,
			message,
			value,
			threshold,
			action))
	}

	return csvBuilder.String()
}

// 简单的发送 HTML 邮件辅助函数 (添加到 monitor_func_v2.3.0.go 或 smas_v2.3.0.go)
// sendRawHtmlEmail 发送不带模板的 HTML 邮件
func sendRawHtmlEmail(emailConfig EmailConfig, subject string, htmlBody string) error {
	// 1. 组装邮件 Header
	headers := make(map[string]string)
	headers["From"] = emailConfig.From

	// 处理多个收件人
	toHeader := strings.Join(emailConfig.Recipients, ",")
	headers["To"] = toHeader

	// Subject 需要进行编码处理，防止中文乱码
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	headers["Subject"] = encodedSubject

	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = "text/html; charset=UTF-8"
	headers["Content-Transfer-Encoding"] = "base64"

	// 2. 组装邮件内容
	message := ""
	for k, v := range headers {
		message += fmt.Sprintf("%s: %s\r\n", k, v)
	}

	// Body 也使用 Base64 编码，避免特殊字符问题
	encodedBody := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	message += "\r\n" + encodedBody

	// 3. 建立连接并发送
	auth := smtp.PlainAuth("", emailConfig.From, emailConfig.Password, emailConfig.SMTPHost)
	addr := fmt.Sprintf("%s:%s", emailConfig.SMTPHost, emailConfig.SMTPPort)

	// 注意：如果您的 SMTP 服务器使用 SSL (通常端口 465)，需要使用 tls.Dial
	// 如果是 TLS/StartTLS (通常端口 587)，可以直接用 smtp.SendMail

	// 这里复用您 smas_v2.3.0.go 中 createSMTPConnection 的逻辑来处理 SSL
	// 为了简单直接，我们在这里手动处理一次 TLS 连接发送
	if emailConfig.SMTPPort == "465" {
		return sendMailOverSSL(addr, auth, emailConfig.From, emailConfig.Recipients, []byte(message))
	}

	// 非 SSL 端口 (如 25 或 587) 直接发送
	return smtp.SendMail(addr, auth, emailConfig.From, emailConfig.Recipients, []byte(message))
}

// sendMailOverSSL 专用于 465 端口的 SSL 发送辅助函数
func sendMailOverSSL(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	// 跳过证书验证
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         strings.Split(addr, ":")[0],
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, strings.Split(addr, ":")[0])
	if err != nil {
		return err
	}
	defer client.Quit()

	if auth != nil {
		if ok, _ := client.Extension("AUTH"); ok {
			if err = client.Auth(auth); err != nil {
				return err
			}
		}
	}

	if err = client.Mail(from); err != nil {
		return err
	}

	for _, addr := range to {
		if err = client.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	return client.Quit()
}

// getTrendIcon 获取趋势图标
func getTrendIcon(level string) string {
	switch level {
	case "critical":
		return "🔴"
	case "severe":
		return "🟠"
	case "warning":
		return "🟡"
	case "recovery":
		return "🟢"
	default:
		return "⚪"
	}
}

// getLevelColor 获取级别颜色
func getLevelColor(level string) string {
	switch level {
	case "critical":
		return "#ff4d4f"
	case "severe":
		return "#fa8c16"
	case "warning":
		return "#faad14"
	case "recovery":
		return "#52c41a"
	default:
		return "#8c8c8c"
	}
}

// getTrendIndicator 获取趋势指示器
func getTrendIndicator(level string, count int) string {
	if count == 0 {
		return "➖"
	} else if count > 5 {
		return "📈"
	} else if count > 2 {
		return "➡️"
	} else {
		return "📉"
	}
}

// getMainIssueForType 获取类型的主要问题
func getMainIssueForType(typ string, messageStats map[string]int) string {
	// 根据类型返回常见问题
	issues := map[string]string{
		"CPU":     "使用率过高",
		"Memory":  "内存不足",
		"Disk":    "磁盘空间不足",
		"Port":    "端口不通",
		"Ping":    "网络不通",
		"Process": "进程异常",
		"Dir":     "目录文件缺失",
	}
	return issues[typ]
}

// rankIPsByAlerts 对IP按告警数排序
func rankIPsByAlerts(ipStats map[string]IPStat) []string {
	type ipCount struct {
		ip    string
		count int
	}

	var ips []ipCount
	for ip, stat := range ipStats {
		ips = append(ips, ipCount{ip, stat.Total})
	}

	// 按告警数降序排序
	sort.Slice(ips, func(i, j int) bool {
		return ips[i].count > ips[j].count
	})

	var result []string
	for _, item := range ips {
		result = append(result, item.ip)
	}
	return result
}

// getRankIcon 获取排名图标
func getRankIcon(rank int) string {
	icons := []string{"🥇", "🥈", "🥉", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣", "🔟"}
	if rank <= len(icons) {
		return icons[rank-1]
	}
	return fmt.Sprintf("%d", rank)
}

// getMainProblemForIP 获取IP的主要问题
func getMainProblemForIP(ipStat IPStat) string {
	// 找出最频繁的告警类型
	var maxType string
	maxCount := 0
	for typ, count := range ipStat.TypeBreakdown {
		if count > maxCount {
			maxType = typ
			maxCount = count
		}
	}

	if maxType != "" {
		return fmt.Sprintf("%s问题(%d次)", maxType, maxCount)
	}
	return "多种问题"
}

// getSortedHours 按小时排序
func getSortedHours(timeStats map[string]int) []string {
	var hours []string
	for hour := range timeStats {
		hours = append(hours, hour)
	}
	sort.Strings(hours)
	return hours
}

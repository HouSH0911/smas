package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// *** 新增：记录告警的辅助函数 ***
func recordAlert(level string, data EmailTemplateData) {
	alertHistoryMutex.Lock()
	defer alertHistoryMutex.Unlock()

	// 简单解析一下标题作为类型，例如 "CPU告警" -> "CPU"
	// 您也可以在 EmailTemplateData 里加一个 Type 字段来传递，这里简化处理
	alertType := "System"
	if strings.Contains(data.Subject, "CPU") {
		alertType = "CPU"
	} else if strings.Contains(data.Subject, "内存") {
		alertType = "Memory"
	} else if strings.Contains(data.Subject, "磁盘") {
		alertType = "Disk"
	} else if strings.Contains(data.Subject, "服务器") {
		alertType = "Ping"
	} else if strings.Contains(data.Subject, "端口") {
		alertType = "Port"
	} else if strings.Contains(data.Subject, "目录") {
		alertType = "directory"
	} else if strings.Contains(data.Subject, "进程") {
		alertType = "Process"
	}

	record := AlertRecord{
		Time:       data.Timestamp,
		Server:     data.Server,
		AlertLevel: level,
		Type:       alertType,
		Message:    data.Message,
	}
	alertHistory = append(alertHistory, record)
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

	// 准备标题
	title := fmt.Sprintf("%s (共 %d 条告警)", config.SummaryReport.Title, count)
	nowStr := time.Now().Format("2006-01-02 15:04:05")

	// ==========================================
	// A. 发送邮件 (HTML 表格格式)
	// ==========================================
	if config.AlertMethods.Email && config.EnableEmail {
		// 构建 HTML 表格
		htmlContent := "<h3>" + title + "</h3>"
		htmlContent += fmt.Sprintf("<p style='color:gray; font-size:12px;'>统计时间: %s</p>", nowStr)
		htmlContent += "<table border='1' cellspacing='0' cellpadding='5' style='border-collapse: collapse; width: 100%; font-size: 13px; font-family: Arial, sans-serif;'>"

		// 表头
		htmlContent += "<tr style='background-color: #f2f2f2; text-align: left;'>"
		htmlContent += "<th>时间</th><th>服务器</th><th>级别</th><th>类型</th><th>内容</th></tr>"

		// 遍历记录填充表格
		for _, r := range records {
			// 根据级别设置简单的颜色样式
			rowStyle := ""
			statusColor := "black"
			if r.AlertLevel == "critical" || r.AlertLevel == "severe" {
				statusColor = "#d9534f"                 // 红色
				rowStyle = "background-color: #fff5f5;" // 浅红背景
			} else if r.AlertLevel == "recovery" {
				statusColor = "#5cb85c" // 绿色
			} else if r.AlertLevel == "warning" {
				statusColor = "#f0ad4e" // 橙色
			}

			htmlContent += fmt.Sprintf("<tr style='%s'>", rowStyle)
			htmlContent += fmt.Sprintf("<td>%s</td>", r.Time)
			htmlContent += fmt.Sprintf("<td>%s</td>", r.Server)
			htmlContent += fmt.Sprintf("<td style='color:%s; font-weight:bold;'>%s</td>", statusColor, r.AlertLevel)
			htmlContent += fmt.Sprintf("<td>%s</td>", r.Type)
			htmlContent += fmt.Sprintf("<td>%s</td>", r.Message)
			htmlContent += "</tr>"
		}
		htmlContent += "</table>"
		htmlContent += "<p style='font-size:12px; color:gray;'>本邮件由监控系统自动生成，请勿回复。</p>"

		// 启动协程发送邮件，不阻塞主流程
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
	// B. 发送企业微信 (文本表格格式)
	// ==========================================
	if config.AlertMethods.WechatWork && config.WechatWork.Enabled {
		// 1. 构建消息头
		mdContent := fmt.Sprintf("# %s\n\n", title)
		mdContent += fmt.Sprintf("> 生成时间: <font color=\"comment\">%s</font>\n\n", nowStr)

		if count == 0 {
			mdContent += "🎉 过去周期内无告警，一切正常！"
		} else {
			// 2. 构建文本表格
			mdContent += "## 告警详情：\n"
			mdContent += "```\n" // 使用代码块确保等宽字体

			// 修改列宽定义，考虑到中文字符宽度
			const (
				timeWidth    = 24 // 不变
				serverWidth  = 16 // 不变
				levelWidth   = 15 // 减少2个字符宽度（因为"级别"2个中文字符，显示宽度为4）
				typeWidth    = 8  // 不变（"类型"2个中文字符，显示宽度为4，但列宽设为8）
				contentWidth = 46 // 减少2个字符宽度（因为"内容"2个中文字符，显示宽度为4）
			)

			// 表格标题行 - 使用固定的格式字符串
			mdContent += "时间                           服务器               级别              类型       内容                                            \n"

			// 分隔线 - 手动计算正确的长度
			separator := strings.Repeat("-", 24) + " " +
				strings.Repeat("-", 16) + " " +
				strings.Repeat("-", 14) + " " +
				strings.Repeat("-", 8) + " " +
				strings.Repeat("-", 46)
			mdContent += separator + "\n"

			// 数据行
			for _, r := range records {
				// 处理时间
				timeStr := r.Time
				// if len(timeStr) > 16 {
				// 	timeStr = timeStr[5:16] // 简化为 "月-日 时:分:秒"
				// }

				// 处理级别（添加表情）
				levelStr := r.AlertLevel
				switch r.AlertLevel {
				case "critical", "severe":
					levelStr = "🔴" + r.AlertLevel
				case "warning":
					levelStr = "🟡" + r.AlertLevel
				case "recovery":
					levelStr = "🟢" + r.AlertLevel
				}

				// 处理内容（自动换行）
				messageLines := wrapText(r.Message, contentWidth)

				// 如果是多行内容，第一行显示完整信息，后续行只显示内容
				for i, line := range messageLines {
					if i == 0 {
						// 第一行显示所有列
						mdContent += fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s\n",
							timeWidth, truncateText(timeStr, timeWidth),
							serverWidth, truncateText(r.Server, serverWidth),
							levelWidth, truncateText(levelStr, levelWidth),
							typeWidth, truncateText(r.Type, typeWidth),
							contentWidth, truncateText(line, contentWidth))
					} else {
						// 后续行只显示内容（其他列留空）
						mdContent += fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s\n",
							timeWidth, "",
							serverWidth, "",
							levelWidth, "",
							typeWidth, "",
							contentWidth, truncateText(line, contentWidth))
					}
				}

				// 在每条记录之间添加空行（可选，提高可读性）
				if len(messageLines) > 1 {
					mdContent += fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s\n",
						timeWidth, "",
						serverWidth, "",
						levelWidth, "",
						typeWidth, "",
						contentWidth, "")
				}
			}

			mdContent += "```\n\n"

			// 3. 添加统计信息
			mdContent += "## 统计摘要\n"

			// 按级别统计
			levelStats := make(map[string]int)
			for _, r := range records {
				levelStats[r.AlertLevel]++
			}

			mdContent += "**告警级别统计:**\n"
			for level, count := range levelStats {
				emoji := "⚪"
				switch level {
				case "critical":
					emoji = "🔴"
				case "severe":
					emoji = "🟠"
				case "warning":
					emoji = "🟡"
				case "recovery":
					emoji = "🟢"
				}
				percentage := float64(count) / float64(len(records)) * 100
				mdContent += fmt.Sprintf("%s %s: %d 条 (%.1f%%)\n", emoji, level, count, percentage)
			}

			// 按类型统计
			typeStats := make(map[string]int)
			for _, r := range records {
				typeStats[r.Type]++
			}

			mdContent += "\n**告警类型统计:**\n"
			for typ, count := range typeStats {
				percentage := float64(count) / float64(len(records)) * 100
				mdContent += fmt.Sprintf(" %s: %d 条 (%.1f%%)\n", typ, count, percentage)
			}

			// 按服务器统计
			serverStats := make(map[string]int)
			for _, r := range records {
				serverStats[r.Server]++
			}

			mdContent += "\n**服务器告警统计:**\n"
			for server, count := range serverStats {
				percentage := float64(count) / float64(len(records)) * 100
				mdContent += fmt.Sprintf("%s: %d 条 (%.1f%%)\n", server, count, percentage)
			}
		}

		// 4. 发送请求
		msg := WechatWorkMessage{
			MsgType: "markdown",
			Markdown: struct {
				Content string `json:"content"`
			}{Content: mdContent},
		}

		go sendWechatWorkRequest(config.WechatWork.WebhookUrl, msg)
	}
}

// *** 新增：文本换行函数 ***
// wrapText 将长文本按指定宽度换行
func wrapText(text string, width int) []string {
	if len(text) <= width {
		return []string{text}
	}

	var lines []string
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	currentLine := words[0]

	for _, word := range words[1:] {
		if len(currentLine)+len(word)+1 <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	lines = append(lines, currentLine)

	return lines
}

// *** 新增：文本截断函数 ***
// truncateText 截断文本并添加省略号（如果需要）
func truncateText(text string, maxWidth int) string {
	if len(text) <= maxWidth {
		return text
	}

	if maxWidth <= 3 {
		return text[:maxWidth]
	}

	return text[:maxWidth-3] + "..."
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

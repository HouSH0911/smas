package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// 企业微信消息构建函数
func buildWechatMessage(alertLevel string, data EmailTemplateData) WechatWorkMessage {
	var message WechatWorkMessage

	// 使用markdown格式
	message.MsgType = "markdown"

	// 根据告警级别设置不同的标题和样式
	var title, emoji, color string
	switch alertLevel {
	case "critical":
		title = "🚨🚨🚨 紧急告警"
		emoji = "🚨"
		color = "#ff4d4d"
	case "severe":
		title = "⚠️ 严重告警"
		emoji = "⚠️"
		color = "#ff9900"
	case "warning":
		title = "⚠️ 一般告警"
		emoji = ""
		color = "#ffcc00"
	case "recovery":
		title = "✅ 恢复通知"
		emoji = "✅"
		color = "#4CAF50"
	default:
		title = "ℹ️ 通知"
		emoji = "ℹ️"
		color = "#1890ff"
	}

	// 构建markdown内容
	markdown := fmt.Sprintf("# %s %s\n", emoji, data.Subject)
	markdown += fmt.Sprintf("> **告警时间**: %s  \n", data.Timestamp)
	markdown += fmt.Sprintf("> **服务器地址**: %s  \n", data.Server)
	markdown += fmt.Sprintf("> **告警级别**: <font color=\"%s\">%s</font>\n\n", color, title)

	markdown += "--------------------------------------------------------------------------\n\n"
	markdown += fmt.Sprintf("**告警详情: **%s\n\n", data.Message)

	if data.Value != "" {
		markdown += fmt.Sprintf("**📈 监控指标**\n\n当前值: `%s`", data.Value)
		if data.Threshold != "" {
			markdown += fmt.Sprintf("  | 阈值: `%s`\n\n", data.Threshold)
		} else {
			markdown += "\n\n"
		}
	}

	if data.Action != "" {
		markdown += fmt.Sprintf("**建议操作: **%s\n", data.Action)
	}

	// 添加优先级提示
	switch alertLevel {
	case "critical":
		markdown += "<font color=\"#ff4d4d\">**🔴 最高优先级 | 需立即处理**</font>"
	case "severe":
		markdown += "<font color=\"#ff9900\">**🟠 高优先级 | 请尽快处理**</font>"
	}

	markdown += "\n--------------------------------------------------------------------------\n*来自: 服务器监控告警系统*"

	message.Markdown.Content = markdown
	return message
}

// 发送企业微信消息
func sendWechatWorkAlert(wechatConfig WechatWorkConfig, alertLevel string, data EmailTemplateData) {
	if !wechatConfig.Enabled {
		return
	}

	// 构建企业微信消息
	message := buildWechatMessage(alertLevel, data)

	// 如果有@提醒，在markdown消息中添加@信息
	if len(wechatConfig.MentionedList) > 0 || len(wechatConfig.MentionedMobileList) > 0 {
		// 构建@文本
		mentionedText := ""
		if len(wechatConfig.MentionedList) > 0 {
			for _, user := range wechatConfig.MentionedList {
				mentionedText += fmt.Sprintf("<@%s> ", user)
			}
		}
		if len(wechatConfig.MentionedMobileList) > 0 {
			for _, mobile := range wechatConfig.MentionedMobileList {
				mentionedText += fmt.Sprintf("<@%s> ", mobile)
			}
		}

		// 在markdown内容开头插入@提醒
		if mentionedText != "" {
			message.Markdown.Content = mentionedText + "\n\n" + message.Markdown.Content
		}
	}

	// 始终发送markdown消息
	sendWechatWorkRequest(wechatConfig.WebhookUrl, message)
}

// 通用的HTTP请求发送函数
func sendWechatWorkRequest(webhookUrl string, message WechatWorkMessage) {
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("序列化企业微信消息失败: %v", err)
		return
	}

	resp, err := http.Post(webhookUrl, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("发送企业微信消息失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("企业微信接口返回错误: %d, 响应: %s", resp.StatusCode, string(body))
		return
	}

	log.Printf("企业微信消息发送成功")
}

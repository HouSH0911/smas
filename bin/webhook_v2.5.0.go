package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
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

	// 根据配置选择使用代理还是直连
	var targetUrl string
	if wechatConfig.ProxyEnabled && wechatConfig.ProxyUrl != "" {
		// 使用代理：解析原始URL获取key，然后构建代理URL
		key := extractKeyFromWebhookUrl(wechatConfig.WebhookUrl)
		targetUrl = fmt.Sprintf("%s/webhook?key=%s", wechatConfig.ProxyUrl, key)
	} else {
		// 直连：使用原始webhookUrl
		targetUrl = wechatConfig.WebhookUrl
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
	sendWechatWorkRequest(targetUrl, message)
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

// 核心函数：上传文件并发送
func sendWechatWorkFile(webhookUrl string, filename string, content []byte) {
	// 1. 从 webhookUrl 中解析 key
	// URL 格式通常是: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx-xxxx
	// 从webhookUrl中提取key
	key := extractKeyFromWebhookUrl(webhookUrl)
	if key == "" {
		key = "cc9c86c9-8dbe-4c39-8970-f71cdbec319d"
	}
	// 根据配置选择使用代理还是直连
	var uploadUrl string

	if config.WechatWork.ProxyEnabled && config.WechatWork.ProxyUrl != "" {
		// 使用代理
		uploadUrl = fmt.Sprintf("%s/upload?key=%s&type=file", config.WechatWork.ProxyUrl, key)
	} else {
		// 直连
		uploadUrl = fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/upload_media?key=%s&type=file", key)
	}

	// 3. 构造 multipart 表单上传文件
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// 创建表单文件字段 "media"
	part, err := writer.CreateFormFile("media", filename)
	if err != nil {
		log.Printf("创建表单失败: %v", err)
		return
	}
	part.Write(content)
	writer.Close() // 必须关闭以写入结尾边界

	// 4. 执行上传请求
	req, _ := http.NewRequest("POST", uploadUrl, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("上传文件请求失败: %v", err)
		return
	}
	defer resp.Body.Close()

	// 5. 解析响应获取 media_id
	respBody, _ := io.ReadAll(resp.Body)
	var mediaResp WechatMediaResponse
	if err := json.Unmarshal(respBody, &mediaResp); err != nil {
		log.Printf("解析上传响应失败: %v", err)
		return
	}

	if mediaResp.MediaId == "" {
		log.Printf("上传文件失败，未获得 media_id。API响应: %s", string(respBody))
		return
	}

	sendWechatWorkRequest(webhookUrl, WechatWorkMessage{
		MsgType: "file",
		File: struct {
			MediaId string `json:"media_id"`
		}{MediaId: mediaResp.MediaId},
	})

	log.Printf("已通过企业微信发送汇总文件: %s", filename)
}

// 辅助函数：从webhook URL中提取key
func extractKeyFromWebhookUrl(webhookUrl string) string {
	u, err := url.Parse(webhookUrl)
	if err != nil {
		return ""
	}
	return u.Query().Get("key")
}

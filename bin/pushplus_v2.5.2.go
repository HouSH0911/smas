package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// PushPlus消息结构
type PushPlusMessage struct {
	Token    string `json:"token"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Template string `json:"template,omitempty"` // html/json/markdown等
	Topic    string `json:"topic,omitempty"`
}

// 构建PushPlus消息内容
func buildPushPlusContent(alertLevel string, data EmailTemplateData) (string, string) {
	var title, content string

	// 根据告警级别设置标题
	switch alertLevel {
	case "critical":
		title = fmt.Sprintf("🚨 紧急告警 - %s", data.Subject)
	case "severe":
		title = fmt.Sprintf("⚠️ 严重告警 - %s", data.Subject)
	case "warning":
		title = fmt.Sprintf("⚠️ 一般告警 - %s", data.Subject)
	case "recovery":
		title = fmt.Sprintf("✅ 恢复通知 - %s", data.Subject)
	case "info":
		title = data.Subject
	default:
		title = fmt.Sprintf("ℹ️ 通知 - %s", data.Subject)
	}

	// 构建HTML内容
	if alertLevel == "info" {
		content = fmt.Sprintf(`<h2>%s</h2><pre>%s</pre><hr><p><i>来自: 服务器监控告警系统</i></p>`,
			data.Subject, data.Message)
	} else {
		content = fmt.Sprintf(`<h2>%s</h2>
<p><strong>告警时间:</strong> %s</p>
<p><strong>服务器地址:</strong> %s</p>
<p><strong>机房:</strong> %s</p>
<p><strong>告警级别:</strong> %s</p>
<hr>
<p><strong>告警详情:</strong> %s</p>`,
			data.Subject, data.Timestamp, data.Server, data.Datacenter, alertLevel, data.Message)

		if data.Value != "" {
			content += fmt.Sprintf(`<p><strong>当前值:</strong> %s`, data.Value)
			if data.Threshold != "" {
				content += fmt.Sprintf(` | <strong>阈值:</strong> %s`, data.Threshold)
			}
			content += `</p>`
		}

		if data.Action != "" {
			content += fmt.Sprintf(`<p><strong>建议操作:</strong> %s</p>`, data.Action)
		}

		content += `<hr><p><i>来自: 服务器监控告警系统</i></p>`
	}

	return title, content
}

// 发送PushPlus告警
func sendPushPlusAlert(pushPlusConfig PushPlusConfig, alertLevel string, data EmailTemplateData) {
	if !pushPlusConfig.Enabled || pushPlusConfig.Token == "" {
		return
	}

	title, content := buildPushPlusContent(alertLevel, data)

	message := PushPlusMessage{
		Token:    pushPlusConfig.Token,
		Title:    title,
		Content:  content,
		Template: "html",
		Topic:    pushPlusConfig.Topic,
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("序列化PushPlus消息失败: %v", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://www.pushplus.plus/send", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("发送PushPlus消息失败: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err == nil {
		if code, ok := result["code"].(float64); ok && code == 200 {
			log.Printf("PushPlus消息发送成功")
		} else {
			log.Printf("PushPlus接口返回错误: %s", string(body))
		}
	} else {
		log.Printf("PushPlus响应解析失败: %s", string(body))
	}
}

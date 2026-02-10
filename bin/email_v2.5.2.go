package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"log"
	"mime"
	"net/smtp"
	"strings"
	"time"
)

// 初始化连接池 (在main函数中调用)
func initSMTPPool(emailConfig EmailConfig, poolSize int) {
	smtpClientPool = make(chan *smtp.Client, poolSize)

	for i := 0; i < poolSize; i++ {
		conn, err := createSMTPConnection(emailConfig)
		if err == nil {
			smtpClientPool <- conn
		}
	}
}

// 创建持久化连接
func createSMTPConnection(emailConfig EmailConfig) (*smtp.Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	conn, err := tls.Dial("tcp", emailConfig.SMTPHost+":"+emailConfig.SMTPPort, tlsConfig)
	if err != nil {
		return nil, err
	}

	client, err := smtp.NewClient(conn, emailConfig.SMTPHost)
	if err != nil {
		return nil, err
	}

	auth := smtp.PlainAuth("", emailConfig.From, emailConfig.Password, emailConfig.SMTPHost)
	if err := client.Auth(auth); err != nil {
		return nil, err
	}

	return client, nil
}

// 从连接池获取客户端
func getSMTPClient() (*smtp.Client, error) {
	select {
	case client := <-smtpClientPool:
		return client, nil
	default:
		return nil, fmt.Errorf("连接池耗尽")
	}
}

// 归还连接到连接池
func returnSMTPClient(client *smtp.Client) {
	smtpClientPool <- client
}

// 初始化邮件队列 (在main函数中调用)
func initEmailQueue(workers int) {
	emailQueue = &EmailQueue{
		queue: make(chan emailTask, 1000), // 缓冲队列
	}

	// 限速器：产生令牌
	tokenBucket := make(chan struct{}, 1)
	go func() {
		// 1. 立即注入第一个令牌，防止启动延迟
		tokenBucket <- struct{}{}
		//ticker := time.NewTicker(time.Duration(config.EmailRateLimit) * time.Second)
		for {
			// 2. 从全局配置中安全地读取当前的速率
			// (注意: 这假设您的 reloadConfig 函数在写入 config 变量时使用了 configMutex.Lock())

			configMutex.RLock() // <-- 如果 reloadConfig 加了写锁，这里需要读锁
			var rate int
			if config.EmailRateLimit > 0 {
				rate = config.EmailRateLimit
			} else {
				rate = 30 // 默认值，防止配置项为0导致CPU空转
			}
			configMutex.RUnlock()

			// 3. 睡眠 "当前" 配置指定的时间
			time.Sleep(time.Duration(rate) * time.Second)

			// 4. 补充令牌
			select {
			case tokenBucket <- struct{}{}: // 每隔 'rate' 秒发一个令牌
			default:
				// 如果令牌桶里已经有一个令牌了，就跳过，防止累积
			}
		}
	}()

	// 启动多个 worker
	for i := 0; i < workers; i++ {
		emailQueue.wg.Add(1)
		go func(id int) {
			defer emailQueue.wg.Done()
			for task := range emailQueue.queue {
				<-tokenBucket // ⏳ 等待令牌（保证全局限速）
				sendEmailInternal(task.emailConfig, task.alertLevel, task.data)
			}
		}(i)
	}
}

// 添加邮件任务到队列
func (q *EmailQueue) AddTask(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	q.queue <- emailTask{emailConfig, alertLevel, data}
}

// 关闭队列 (在main退出时调用)
func (q *EmailQueue) Close() {
	close(q.queue)
	q.wg.Wait()
}

// 外部调用接口 (替换原sendEmail)
func sendEmail(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	if !config.EnableEmail {
		log.Printf("邮件发送被禁用，告警内容仅记录日志: %s", data.Subject)
		return
	}
	// 添加任务到队列
	if emailQueue != nil {
		emailQueue.AddTask(emailConfig, alertLevel, data)
	} else {
		// 降级处理：直接发送（不推荐）
		sendEmailInternal(emailConfig, alertLevel, data)
	}
}

// 发送邮件函数（两个变量：邮件配置结构体声明的对象、主题、邮件内容）
// 发送邮件函数（带健康检查和连接重建）
func sendEmailInternal(emailConfig EmailConfig, alertLevel string, data EmailTemplateData) {
	if data.Timestamp == "" {
		data.Timestamp = time.Now().Format("2006-01-02 15:04:05")
	}

	// 根据告警级别选择模板
	templateName := ""
	switch alertLevel {
	case "critical":
		templateName = "critical_alert.html"
	case "severe":
		templateName = "severe_alert.html"
	case "warning":
		templateName = "warning_alert.html"
	case "recovery":
		templateName = "recovery_alert.html"
	default:
		log.Printf("Unknown alert level: %s", alertLevel)
		return
	}

	// 渲染模板
	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, templateName, data); err != nil {
		log.Printf("Failed to execute template: %v", err)
		return
	}

	from := emailConfig.From
	to := strings.Join(emailConfig.Recipients, ",")
	encodedSubject := mime.QEncoding.Encode("utf-8", data.Subject)

	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   encodedSubject,
		"MIME-Version":              "1.0",
		"Content-Type":              "text/html; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
	}

	var emailContent bytes.Buffer
	for k, v := range headers {
		fmt.Fprintf(&emailContent, "%s: %s\r\n", k, v)
	}
	fmt.Fprintf(&emailContent, "\r\n")
	emailContent.Write(body.Bytes())

	// 获取SMTP客户端（优先池子）
	client, err := getSMTPClient()
	if err != nil {
		// 池子空了 → 新建一个
		client, err = createSMTPConnection(emailConfig)
		if err != nil {
			log.Printf("创建SMTP连接失败: %v", err)
			return
		}
		//defer client.Close()
		defer returnSMTPClient(client)
	} else {
		// 检查连接是否健康
		if err := client.Noop(); err != nil {
			log.Printf("SMTP连接失效，重建中: %v", err)
			client.Close()
			client, err = createSMTPConnection(emailConfig)
			if err != nil {
				log.Printf("重建SMTP连接失败: %v", err)
				return
			}
			//defer client.Close()
			defer returnSMTPClient(client)
		} else {
			// 连接是健康的 → 用完还回池子
			defer returnSMTPClient(client)
		}
	}

	// 设置发件人
	if err := client.Mail(emailConfig.From); err != nil {
		log.Printf("设置发件人失败: %v，尝试重建连接", err)
		client.Close()
		client, err = createSMTPConnection(emailConfig)
		if err != nil {
			log.Printf("重建SMTP连接失败: %v", err)
			return
		}
		defer returnSMTPClient(client)

		if err := client.Mail(emailConfig.From); err != nil {
			log.Printf("重建连接后仍设置发件人失败: %v", err)
			return
		}
	}

	// 设置收件人
	for _, recipient := range emailConfig.Recipients {
		if err := client.Rcpt(recipient); err != nil {
			log.Printf("设置收件人失败: %v", err)
			continue
		}
	}

	// 写入数据
	wc, err := client.Data()
	if err != nil {
		log.Printf("获取邮件数据流失败: %v", err)
		return
	}
	defer wc.Close()

	if _, err := wc.Write(emailContent.Bytes()); err != nil {
		log.Printf("写入邮件数据失败: %v", err)
		return
	}

	log.Printf("邮件发送成功: %s", data.Subject)
}

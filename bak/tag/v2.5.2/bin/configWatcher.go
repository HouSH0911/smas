package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	config      Config
	configMutex sync.RWMutex
	configPath  string
)

func reloadConfig() error {
	configMutex.Lock() // 写锁
	defer configMutex.Unlock()

	log.Println("Reloading configuration...")

	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config.json not found in %s", filepath.Dir(configPath))
	}

	// 读取配置文件
	configFile, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading config file: %v", err)
	}

	// 解析JSON
	var newConfig Config
	if err := json.Unmarshal(configFile, &newConfig); err != nil {
		return fmt.Errorf("error parsing config file: %v", err)
	}

	// 解析地址范围
	for i := range newConfig.Servers {
		expandedAddrs, err := parseAddresses(newConfig.Servers[i].Addresses)
		if err != nil {
			return fmt.Errorf("error parsing addresses: %v", err)
		}
		newConfig.Servers[i].Addresses = expandedAddrs
	}

	// 更新全局配置
	config = newConfig
	log.Printf("配置已重新加载 -----------\n 邮件告警开关: %v \n 企业微信告警开关：%v \n"+
		"邮件相关部分：---------------------------------\n 邮件发送: %v \n邮件接收：%v \n 邮件发送间隔：%vs \n"+
		"相关检测开关：---------------------------------\n 进程监控开关: %v \n 端口监控开关: %v \n 服务器通信监控开关: %v \n 资源监控开关: %v \n 目录文件监控开关: %v \n 目标服务器端口监控开关: %v \n 资源告警平滑指数：%v \n 故障冷却时间：%vs \n"+
		"企业微信设置：---------------------------------\n 企业微信告警发送开关: %v \n 机器人接口地址: %v \n 企业微信服务代理开关: %v \n 企业微信服务代理地址: %v \n"+
		"汇报相关部分：---------------------------------\n 汇总报告开关： %v \n 汇总报告发送频率：%v \n 汇总报告发送时间：%v \n"+
		"话单监控部分：---------------------------------\n 话单监控开关: %v \n 话单报告发送时间: %v \n",
		config.AlertMethods.Email, config.AlertMethods.WechatWork,
		config.Email.From, config.Email.Recipients, config.EmailRateLimit,
		config.Monitor.ProcessMonitor, config.Monitor.PortMonitor, config.Monitor.ServerReachMonitor, config.Monitor.Resource, config.Monitor.DirFileMonitor, config.Monitor.RaPortMonitor, config.ResourceSmooth, config.FailureCooldown,
		config.WechatWork.Enabled, config.WechatWork.WebhookUrl, config.WechatWork.ProxyEnabled, config.WechatWork.ProxyUrl,
		config.SummaryReport.Enabled, config.SummaryReport.ReportType, config.SummaryReport.ReportTime,
		config.StreamReport.Enabled, config.StreamReport.ReportTime,
	)
	return nil
}

// 监控配置文件
func startConfigWatcher() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Error creating watcher: %v", err)
	}
	defer watcher.Close()

	configDir := filepath.Dir(configPath)
	// 监控目录而不是单个文件
	if err := watcher.Add(configDir); err != nil {
		log.Fatalf("Error watching config directory: %v", err)
	}
	log.Printf("监控配置目录: %s", configDir)

	// 防抖计时器
	var debounceTimer *time.Timer
	debounceDelay := 500 * time.Millisecond

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// 只处理目标配置文件的事件
			if filepath.Base(event.Name) != "config.json" {
				continue
			}

			log.Printf("配置文件事件: %s", event)

			// 处理所有相关事件
			if event.Op&(fsnotify.Write|fsnotify.Rename|fsnotify.Create|fsnotify.Chmod) != 0 {
				// 取消之前的定时器
				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				// 设置新的防抖定时器
				debounceTimer = time.AfterFunc(debounceDelay, func() {
					log.Println("配置文件变更，重新加载...")
					if err := reloadConfig(); err != nil {
						log.Printf("重新加载配置失败: %v", err)
					} else {
						log.Println("配置重新加载成功")
					}
				})
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("监控错误: %v", err)
		}
	}
}

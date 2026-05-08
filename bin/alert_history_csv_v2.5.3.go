package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func getLegacyHistoryPath() string {
	return filepath.Join(dataDir, legacyHistoryFileName)
}

func buildAlertHistoryCSV(records []AlertRecord) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")

	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{
		"time", "server", "alert_level", "type", "message", "value", "threshold", "action", "status", "port",
	}); err != nil {
		return nil, err
	}

	for _, record := range records {
		if err := writer.Write([]string{
			record.Time,
			record.Server,
			record.AlertLevel,
			record.Type,
			record.Message,
			record.Value,
			record.Threshold,
			record.Action,
			record.Status,
			record.Port,
		}); err != nil {
			return nil, err
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func parseAlertHistoryCSV(data []byte) ([]AlertRecord, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	reader := csv.NewReader(bytes.NewReader(data))

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []AlertRecord{}, nil
	}

	start := 0
	if len(rows[0]) > 0 && strings.EqualFold(strings.TrimSpace(rows[0][0]), "time") {
		start = 1
	}

	records := make([]AlertRecord, 0, len(rows)-start)
	for _, row := range rows[start:] {
		if len(row) == 0 {
			continue
		}

		record := AlertRecord{}
		if len(row) > 0 {
			record.Time = row[0]
		}
		if len(row) > 1 {
			record.Server = row[1]
		}
		if len(row) > 2 {
			record.AlertLevel = row[2]
		}
		if len(row) > 3 {
			record.Type = row[3]
		}
		if len(row) > 4 {
			record.Message = row[4]
		}
		if len(row) > 5 {
			record.Value = row[5]
		}
		if len(row) > 6 {
			record.Threshold = row[6]
		}
		if len(row) > 7 {
			record.Action = row[7]
		}
		if len(row) > 8 {
			record.Status = row[8]
		}
		if len(row) > 9 {
			record.Port = row[9]
		}

		records = append(records, record)
	}

	return records, nil
}

func saveAlertHistory() {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("创建 data 目录失败: %v", err)
		return
	}

	data, err := buildAlertHistoryCSV(alertHistory)
	if err != nil {
		log.Printf("序列化告警历史为 CSV 失败: %v", err)
		return
	}

	if err := os.WriteFile(getHistoryPath(), data, 0644); err != nil {
		log.Printf("保存告警历史 CSV 文件失败: %v", err)
	}
}

func backupAlertHistoryFile() {
	historyPath := getHistoryPath()
	if _, err := os.Stat(historyPath); os.IsNotExist(err) {
		return
	}

	now := time.Now()
	dateStr := now.Format("20060102")
	reportType := config.SummaryReport.ReportType
	if reportType == "" {
		reportType = "daily"
	}

	backupPath := filepath.Join(dataDir, "alert_history_"+dateStr+"_"+reportType+".csv")
	data, err := os.ReadFile(historyPath)
	if err != nil {
		log.Printf("读取告警历史文件用于备份失败: %v", err)
		return
	}

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		log.Printf("备份告警历史文件失败: %v", err)
		return
	}

	log.Printf("告警历史 CSV 已备份到: %s", backupPath)
}

func LoadAlertHistory() {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("创建 data 目录失败: %v", err)
		return
	}

	alertHistoryMutex.Lock()
	defer alertHistoryMutex.Unlock()

	if data, err := os.ReadFile(getHistoryPath()); err == nil {
		loadedHistory, parseErr := parseAlertHistoryCSV(data)
		if parseErr != nil {
			log.Printf("解析 CSV 告警历史文件失败: %v", parseErr)
			return
		}

		alertHistory = loadedHistory
		log.Printf("成功从 CSV 加载 %d 条历史告警记录", len(alertHistory))
		return
	} else if !os.IsNotExist(err) {
		log.Printf("读取 CSV 告警历史文件失败: %v", err)
		return
	}

	legacyPath := getLegacyHistoryPath()
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		return
	}

	data, err := os.ReadFile(legacyPath)
	if err != nil {
		log.Printf("读取旧 JSON 告警历史文件失败: %v", err)
		return
	}

	var loadedHistory []AlertRecord
	if err := json.Unmarshal(data, &loadedHistory); err != nil {
		log.Printf("解析旧 JSON 告警历史文件失败: %v", err)
		return
	}

	alertHistory = loadedHistory
	log.Printf("成功从旧 JSON 加载 %d 条历史告警记录，下次保存时将自动转为 CSV", len(alertHistory))
}

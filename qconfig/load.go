package qconfig

import (
	"encoding/json"
	"fmt"
	"github.com/kamioair/utils/qio"
	"github.com/spf13/viper"
	"os"
	"strings"
)

// LoadConfig 统一的配置加载方法
// cfgFile: 配置文件路径
// configs: 配置映射，key为配置节名称，value为配置对象
func LoadConfig(cfgFile string, configs map[string]interface{}) error {
	// 切换到配置文件所在目录
	configDir := ""
	if lastSlash := strings.LastIndex(cfgFile, "/"); lastSlash != -1 {
		configDir = cfgFile[:lastSlash]
	} else if lastSlash := strings.LastIndex(cfgFile, "\\"); lastSlash != -1 {
		configDir = cfgFile[:lastSlash]
	}

	if configDir != "" {
		err := os.Chdir(configDir)
		if err != nil {
			return fmt.Errorf("无法切换到配置文件目录: %v", err)
		}
	}

	// 如果配置文件不存在，则创建一个空的配置文件
	if qio.PathExists(cfgFile) == false {
		err := qio.WriteString(cfgFile, "", false)
		if err != nil {
			return fmt.Errorf("无法创建配置文件: %v", err)
		}
	}

	// 初始化 Viper
	viper.SetConfigFile(cfgFile)
	viper.SetConfigType("yaml")
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("无法读取配置文件: %v", err)
	}

	// 从文件中读取配置到对应的对象
	for sectionName, configObj := range configs {
		if err := setModuleConfig(sectionName, configObj); err != nil {
			return fmt.Errorf("加载配置节 %s 失败: %v", sectionName, err)
		}
	}

	return nil
}

// setModuleConfig 设置配置节
func setModuleConfig(sectionName string, configObj interface{}) error {
	value := viper.Get(sectionName)
	if value == nil {
		return nil
	}

	js, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return json.Unmarshal(js, configObj)
}

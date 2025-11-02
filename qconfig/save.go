package qconfig

import (
	"fmt"
	"github.com/kamioair/utils/qio"
	"strings"
)

// SaveConfigOptions 保存配置到文件
type SaveConfigOptions struct {
	SectionDescs  map[string]string // 配置节描述映射，key为配置节名称，value为描述
	ExcludeFields []string          // 需要排除的字段列表
}

// 需要保存的内容
var saveConfigs = map[string]any{}

// SaveConfig 保存配置文件
// filePath: 配置文件路径
// configs: 配置映射，key为配置节名称，value为配置对象
// opts: 保存选项，包含配置节描述和排除字段（可为空）
func SaveConfig(filePath string, opts *SaveConfigOptions) error {
	// 生成Base配置内容
	configBase := map[string]any{}
	if baseConfig, exists := saveConfigs["Base"]; exists {
		configBase["Base"] = baseConfig
	}

	// 初始值
	if opts == nil {
		opts = &SaveConfigOptions{}
	}
	if opts.ExcludeFields == nil {
		opts.ExcludeFields = []string{}
	}

	newCfg := ""
	if len(configBase) > 0 {
		newCfg += "############################### Base Config ###############################\n"
		newCfg += "# 通用基础配置\n"
		newCfg += toYAML(configBase, 0, opts.ExcludeFields)
	}

	// 生成模块配置内容
	for sectionName, configObj := range saveConfigs {
		if sectionName == "Base" {
			continue // Base配置已经处理过了
		}

		configModule := map[string]any{}
		configModule[sectionName] = configObj

		// 获取配置节描述
		desc := ""
		if opts.SectionDescs != nil {
			if descValue, exists := opts.SectionDescs[sectionName]; exists {
				desc = descValue
			}
		}

		mCfg := fmt.Sprintf("############################### %s Config ###############################\n", sectionName)
		if desc != "" {
			mCfg += fmt.Sprintf("# %s\n", desc)
		}
		mCfg += toYAML(configModule, 0, opts.ExcludeFields)

		if !strings.HasSuffix(mCfg, fmt.Sprintf("%s: \n", sectionName)) {
			if newCfg != "" {
				newCfg += "\n\n"
			}
			newCfg += mCfg
		}
	}

	// 尝试检测是否有变化，如果有则更新文件
	trySave(filePath, newCfg)
	return nil
}

// trySave 尝试保存配置文件，如果配置有变化则更新文件
func trySave(filePath string, newCfg string) {
	oldCfg, _ := qio.ReadAllString(filePath)
	oldBlocks := getBlockValues(oldCfg)
	newBlocks := getBlockValues(newCfg)

	// 创建一个新的切片来存储最终的配置块
	var finalBlocks [][2]string

	// 遍历 oldBlocks，确保顺序
	for _, ob := range oldBlocks {
		exist := false
		// 检查 ob 是否在 newBlocks 中
		for _, nb := range newBlocks {
			// 提取配置块名称进行比较（去除" Config"后缀）
			obName := strings.TrimSpace(strings.TrimSuffix(ob[0], " Config"))
			nbName := strings.TrimSpace(strings.TrimSuffix(nb[0], " Config"))
			if obName == nbName {
				exist = true
				// 如果存在，则使用 newBlocks 中的内容
				finalBlocks = append(finalBlocks, nb)
				break
			}
		}
		// 如果不存在，则使用 oldBlocks 中的内容
		if !exist {
			finalBlocks = append(finalBlocks, ob)
		}
	}

	// 将 newBlocks 中未处理的块追加到 finalBlocks 中
	for _, nb := range newBlocks {
		exist := false
		for _, fb := range finalBlocks {
			if fb[0] == nb[0] {
				exist = true
				break
			}
		}
		if !exist {
			if strings.HasPrefix(nb[1], "#") &&
				strings.Contains(nb[1], "DB Config") {
				tlist := make([][2]string, 0)
				for i := 0; i < len(finalBlocks); i++ {
					if i == 1 {
						tlist = append(tlist, nb)
					}
					tlist = append(tlist, finalBlocks[i])
				}
				finalBlocks = tlist
			} else {
				finalBlocks = append(finalBlocks, nb)
			}
		}
	}

	// 将 finalBlocks 转换为最终的配置字符串
	finalCfg := ""
	for _, fb := range finalBlocks {
		finalCfg += fb[1] + "\n"
	}

	// 如果配置有变化，则保存
	if oldCfg != finalCfg {
		err := qio.WriteString(filePath, finalCfg, false)
		if err != nil {
			panic(err)
		}
	}
}

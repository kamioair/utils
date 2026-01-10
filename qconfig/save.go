package qconfig

import (
	"fmt"
	"github.com/kamioair/utils/qio"
	"strings"
)

// SaveConfig 保存配置文件
// filePath: 配置文件路径
// saveConfigs: 配置映射，key为配置节名称，如Base，模块名称等；value为配置内容
func SaveConfig(filePath string, saveContent SaveContent) error {
	// 生成Base配置内容
	configBase := map[string]saveData{}
	if baseConfig, exists := saveContent.get("Base"); exists {
		configBase["Base"] = baseConfig
	}

	// 先生成Base配置
	newCfg := ""
	if len(configBase) > 0 {
		configModule := map[string]any{}
		configModule["Base"] = configBase["Base"].Content

		newCfg += "############################### Base Config ###############################\n"
		if configBase["Base"].Desc != "" {
			newCfg += fmt.Sprintf("# %s\n", configBase["Base"].Desc)
		}
		newCfg += toYAML(configModule, 0, configBase["Base"].ExcludeFields)
	}

	// 生成模块配置内容
	for _, sectionName := range saveContent.allKeys() {
		if sectionName == "Base" {
			continue // Base配置已经处理过了
		}

		configObj, _ := saveContent.get(sectionName)
		configModule := map[string]any{}
		configModule[sectionName] = configObj.Content

		// 获取配置节描述
		desc := configObj.Desc

		mCfg := fmt.Sprintf("############################### %s Config ###############################\n", sectionName)
		if desc != "" {
			mCfg += fmt.Sprintf("# %s\n", desc)
		}
		mCfg += toYAML(configModule, 0, configObj.ExcludeFields)

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

// SaveContent 配置内容
type SaveContent struct {
	content map[string]saveData
}

type saveData struct {
	Content       any
	Desc          string   // 配置的描述，可为空
	ExcludeFields []string // 需要排除不生成到文件的字段列表，可为空
}

// Add 添加配置
func (sc *SaveContent) Add(sectionName string, sectionDesc string, content any) {
	sc.AddWithExclude(sectionName, sectionDesc, content, nil)
}

// AddWithExclude 添加配置，并指定需要排除的字段
func (sc *SaveContent) AddWithExclude(sectionName string, sectionDesc string, content any, excludeFields []string) {
	if sc.content == nil {
		sc.content = map[string]saveData{}
	}
	sc.content[sectionName] = struct {
		Content       any
		Desc          string
		ExcludeFields []string
	}{
		Content:       content,
		Desc:          sectionDesc,
		ExcludeFields: excludeFields,
	}
}

func (sc *SaveContent) get(sectionName string) (saveData, bool) {
	if sc.content == nil {
		return saveData{}, false
	}
	ctx, exist := sc.content[sectionName]
	return ctx, exist
}

func (sc *SaveContent) allKeys() []string {
	var keys []string
	for k := range sc.content {
		keys = append(keys, k)
	}
	return keys
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

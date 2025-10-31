package qconfig

import (
	"fmt"
	"github.com/kamioair/utils/qio"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

// TrySave 尝试保存配置文件，如果配置有变化则更新文件
func TrySave(filePath string, newCfg string) {
	oldCfg, _ := qio.ReadAllString(filePath)
	oldBlocks := GetBlockValues(oldCfg)
	newBlocks := GetBlockValues(newCfg)

	// 创建一个新的切片来存储最终的配置块
	var finalBlocks [][2]string
	re := regexp.MustCompile(`\([^)]*\)`)

	// 遍历 oldBlocks，确保顺序
	for _, ob := range oldBlocks {
		exist := false
		// 检查 ob 是否在 newBlocks 中
		for _, nb := range newBlocks {
			obV := re.ReplaceAllString(ob[0], "")
			nbV := re.ReplaceAllString(nb[0], "")
			if obV == nbV {
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

// GetBlockValues 从配置字符串中提取配置块
func GetBlockValues(input string) [][2]string {
	// 存储提取的内容
	var configBlocks [][2]string
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "###############################") && strings.HasSuffix(line, "###############################") {
			configBlocks = append(configBlocks, [2]string{strings.Trim(lines[i+1], " "), ""})
		}
		if len(configBlocks) > 0 {
			configBlocks[len(configBlocks)-1][1] += line + "\n"
		}
	}

	return configBlocks
}

// ToYAML 将任意对象转换为YAML格式字符串
func ToYAML(v any, indent int, excludeFields []string) string {
	value := reflect.ValueOf(v)
	return toYAMLValue(value, indent, excludeFields)
}

// toYAMLValue 递归处理YAML值转换
func toYAMLValue(value reflect.Value, indent int, excludeFields []string) string {
	switch value.Kind() {
	case reflect.Struct:
		return toYAMLStruct(value, indent, excludeFields)
	case reflect.Map:
		return toYAMLMap(value, indent, excludeFields)
	case reflect.Slice, reflect.Array:
		return toYAMLSlice(value, indent, excludeFields)
	case reflect.Ptr, reflect.Interface:
		if value.IsNil() {
			return "null"
		}
		return toYAMLValue(value.Elem(), indent, excludeFields)
	case reflect.String:
		// 字符串类型增加双引号
		return fmt.Sprintf("\"%v\"", value.Interface())
	default:
		// 基本类型直接返回值，不换行
		return fmt.Sprintf("%v", value.Interface())
	}
}

// toYAMLStruct 处理结构体到YAML的转换
func toYAMLStruct(value reflect.Value, indent int, excludeFields []string) string {
	var builder strings.Builder
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := typ.Field(i)
		// 如果字段名的首字母是小写，则跳过
		if len(field.Name) > 0 && unicode.IsLower(rune(field.Name[0])) {
			continue
		}
		// 如果字段在排除列表中，则跳过
		if Contains(excludeFields, field.Name) {
			continue
		}
		fieldValue := value.Field(i)
		// 读取字段的注释
		comment := field.Tag.Get("comment")
		// 如果注释存在，则在字段前面插入注释
		if comment != "" {
			// 将注释按换行符拆分，并在每一行前面加上#
			commentLines := strings.Split(comment, "\n")
			for _, line := range commentLines {
				val := fmt.Sprintf("%s# %s\n", strings.Repeat("  ", indent), line)
				builder.WriteString(val)
			}
		}
		// 生成字段名和值
		str1 := fmt.Sprintf("%s%s: ", strings.Repeat("  ", indent), field.Name)
		str2 := toYAMLValue(fieldValue, indent+1, excludeFields)
		builder.WriteString(str1)
		if str2 != "[]" {
			switch fieldValue.Kind() {
			case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Ptr, reflect.Interface:
				builder.WriteString("\n")
			default:

			}
		}
		builder.WriteString(str2)
		if i < value.NumField()-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// toYAMLMap 处理映射到YAML的转换
func toYAMLMap(value reflect.Value, indent int, excludeFields []string) string {
	var builder strings.Builder
	keys := value.MapKeys()
	for i, key := range keys {
		mapValue := value.MapIndex(key)
		str1 := fmt.Sprintf("%s%s: ", strings.Repeat("  ", indent), key.Interface())
		str2 := toYAMLValue(mapValue, indent+1, excludeFields)
		builder.WriteString(str1)
		if str2 != "[]" {
			switch mapValue.Kind() {
			case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Ptr, reflect.Interface:
				builder.WriteString("\n")
			default:

			}
		}
		builder.WriteString(str2)
		if i < len(keys)-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// toYAMLSlice 处理切片/数组到YAML的转换
func toYAMLSlice(value reflect.Value, indent int, excludeFields []string) string {
	var builder strings.Builder
	count := value.Len()
	if count == 0 {
		builder.WriteString("[]")
	} else {
		for i := 0; i < value.Len(); i++ {
			str1 := fmt.Sprintf("%s- ", strings.Repeat("  ", indent))
			str2 := toYAMLValue(value.Index(i), indent+1, excludeFields)
			builder.WriteString(str1)
			builder.WriteString(str2)
			if i < value.Len()-1 {
				builder.WriteString("\n")
			}
		}
	}
	return builder.String()
}

// Contains 判断字符串是否在列表中
func Contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
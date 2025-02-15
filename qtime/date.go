package qtime

import (
	"fmt"
	"github.com/araddon/dateparse"
	"strconv"
	"strings"
	"time"
)

var (
	dateFormat = "yyyy-MM-dd" // 日期掩码
)

// Date 基于整形存储的日期类型，例如：20240101
type Date uint32

// NewDate
//
//	@Description: 创建日期
//	@param t 时间
//	@return Date
func NewDate(t time.Time) Date {
	t = t.Local()
	s := fmt.Sprintf("%04d%02d%02d", t.Year(), t.Month(), t.Day())
	v, _ := strconv.ParseUint(s, 10, 32)
	return Date(v)
}

// ForTo
//
//	@Description: 自动循环遍历日期，并按日返回每天日期
//	@param endDate 结束日期
//	@param interval 日期间隔
//	@param callback 回调
//	@return int YYYY年+WW周，如 202451
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) ForTo(endDate Date, interval uint, callback func(curr Date)) {
	start := d.ToTime()
	end := endDate.ToTime()

	// 判断正序或逆序
	if start.Before(end) || start.Equal(end) {
		// 正序遍历
		for current := start; !current.After(end); current = current.AddDate(0, 0, int(interval)) {
			callback(NewDate(current))
		}
	} else {
		// 逆序遍历
		for current := start; !current.Before(end); current = current.AddDate(0, 0, -int(interval)) {
			callback(NewDate(current))
		}
	}
}

// YearWeek
//
//	@Description: 获取当前日期所在本年的周数
//	@return int YYYY年+WW周，如 202451
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) YearWeek() int {
	y, w := d.ToTime().ISOWeek()
	str := fmt.Sprintf("%d%d", y, w)
	week, _ := strconv.Atoi(str)
	return week
}

// Week
//
//	@Description: 获取当前日期是周几
//	@return int YYYY年+WW周，如 202451
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) Week() time.Weekday {
	return d.ToTime().Weekday()
}

// CurrentToWeekday
//
//	@Description: 获取当前日期所在本周指定周几的日期
//	@param weekday 需要返回周几
//	@return date 周几日期
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) CurrentToWeekday(weekday time.Weekday) Date {
	now := d.ToTime()
	// 获取当前是周几 (0=周日, 1=周一, ..., 6=周六)
	currentWeekday := int(now.Weekday())
	if currentWeekday == 0 {
		currentWeekday = 7 // 将周日看作一周的第 7 天
	}
	// 计算本周的目标周几的偏移天数
	// 如果目标周几比当前周几大，偏移为正；如果比当前小，偏移为负
	offset := int(weekday) - currentWeekday
	// 计算目标日期
	targetDate := now.AddDate(0, 0, offset)
	return NewDate(targetDate)
}

// AddDays
//
//	@Description: 增减天数
//	@param day 天数
//	@return Date
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) AddDays(day int) Date {
	t := d.ToTime()
	t = t.AddDate(0, 0, day)
	return NewDate(t)
}

// AddMonths
//
//	@Description: 增减月数
//	@param month 月数
//	@return Date
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) AddMonths(month int) Date {
	t := d.ToTime()
	t = t.AddDate(0, month, 0)
	return NewDate(t)
}

// AddYears
//
//	@Description: 增减年数
//	@param year 年数
//	@return Date
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) AddYears(year int) Date {
	t := d.ToTime()
	t = t.AddDate(year, 0, 0)
	return NewDate(t)
}

// ToTime
//
//	@Description: 转为原生时间对象
//	@return time.Time
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) ToTime() time.Time {
	if d == 0 {
		return time.Time{}
	}
	str := fmt.Sprintf("%d", d)
	if len(str) != 8 {
		str = str + strings.Repeat("0", 8-len(str))
	}
	year, _ := strconv.Atoi(str[0:4])
	month, _ := strconv.Atoi(str[4:6])
	day, _ := strconv.Atoi(str[6:8])
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
}

// ToString
//
//	@Description: 根据全局format格式化输出
//	@return string
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) ToString() string {
	return d.ToTime().Format(getLayout(dateFormat))
}

// MarshalJSON
//
//	@Description: 复写json转换
//	@return []byte
//	@return error
//
//goland:noinspection GoMixedReceiverTypes
func (d Date) MarshalJSON() ([]byte, error) {
	str := fmt.Sprintf("\"%s\"", d.ToString())
	return []byte(str), nil
}

// UnmarshalJSON
//
//	@Description: 复写json转换
//	@param data
//	@return error
//
//goland:noinspection GoMixedReceiverTypes
func (d *Date) UnmarshalJSON(data []byte) error {
	timeStr := strings.Trim(string(data), "\"")
	v, err := dateparse.ParseLocal(timeStr)
	if err == nil {
		s := fmt.Sprintf("%04d%02d%02d", v.Year(), v.Month(), v.Day())
		t, _ := strconv.ParseUint(s, 10, 64)
		*d = Date(t)
	}
	return err
}

func getLayout(formatStr string) string {
	//"2006-01-02 15:04:05"
	if strings.Contains(formatStr, "yyyy") {
		formatStr = strings.Replace(formatStr, "yyyy", "2006", 1)
	}
	if strings.Contains(formatStr, "yy") {
		formatStr = strings.Replace(formatStr, "yy", "06", 1)
	}
	if strings.Contains(formatStr, "YYYY") {
		formatStr = strings.Replace(formatStr, "YYYY", "2006", 1)
	}
	if strings.Contains(formatStr, "YY") {
		formatStr = strings.Replace(formatStr, "YY", "06", 1)
	}
	if strings.Contains(formatStr, "MM") {
		formatStr = strings.Replace(formatStr, "MM", "01", 1)
	}
	if strings.Contains(formatStr, "M") {
		formatStr = strings.Replace(formatStr, "M", "1", 1)
	}
	if strings.Contains(formatStr, "DD") {
		formatStr = strings.Replace(formatStr, "DD", "02", 1)
	}
	if strings.Contains(formatStr, "D") {
		formatStr = strings.Replace(formatStr, "D", "2", 1)
	}
	if strings.Contains(formatStr, "dd") {
		formatStr = strings.Replace(formatStr, "dd", "02", 1)
	}
	if strings.Contains(formatStr, "d") {
		formatStr = strings.Replace(formatStr, "d", "2", 1)
	}
	if strings.Contains(formatStr, "HH") {
		formatStr = strings.Replace(formatStr, "HH", "15", 1)
	}
	if strings.Contains(formatStr, "H") {
		formatStr = strings.Replace(formatStr, "H", "15", 1)
	}
	if strings.Contains(formatStr, "hh") {
		formatStr = strings.Replace(formatStr, "hh", "15", 1)
	}
	if strings.Contains(formatStr, "h") {
		formatStr = strings.Replace(formatStr, "h", "15", 1)
	}
	if strings.Contains(formatStr, "mm") {
		formatStr = strings.Replace(formatStr, "mm", "04", 1)
	}
	if strings.Contains(formatStr, "m") {
		formatStr = strings.Replace(formatStr, "m", "4", 1)
	}
	if strings.Contains(formatStr, "ss") {
		formatStr = strings.Replace(formatStr, "ss", "05", 1)
	}
	if strings.Contains(formatStr, "s") {
		formatStr = strings.Replace(formatStr, "s", "5", 1)
	}
	if strings.Contains(formatStr, "fff") {
		formatStr = strings.Replace(formatStr, "fff", "000", 1)
	}
	return formatStr
}

package qos

import (
	"github.com/mitchellh/go-ps"
	"strings"
)

// GetProcessCount 获取指定进程数量，按进程名称
func GetProcessCount(name string) int {
	processes, err := ps.Processes()
	if err != nil {
		return 0
	}
	count := 0
	for _, p := range processes {
		pName := strings.ToLower(p.Executable())
		sName := strings.ToLower(name)
		if pName == sName {
			count++
		}
	}
	return count
}

// GetProcessCountByPid 获取指定进程数量，按进程Id
func GetProcessCountByPid(pid int) int {
	processes, err := ps.Processes()
	if err != nil {
		return 0
	}
	count := 0
	for _, p := range processes {
		if p.Pid() == pid {
			count++
		}
	}
	return count
}

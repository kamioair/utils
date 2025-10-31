package qos

import (
	"fmt"
	"net/http"
)

// CheckEmqxClientExist 检测Emqx客户端是否存在
func CheckEmqxClientExist(emqxAddr string, userName string, password string, clientID string) bool {
	url := fmt.Sprintf("%s/api/v5/clients/%s", emqxAddr, clientID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}

	// 设置认证头（如果启用了认证）
	req.SetBasicAuth(userName, password)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true
	}

	return false
}

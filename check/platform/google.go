package platform

import "net/http"

// CheckGoogle 检测Google访问状态
func CheckGoogle(httpClient *http.Client) (bool, error) {
	resp, err := httpClient.Get("http://www.google.com/generate_204")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		return true, nil
	}
	return false, nil
}

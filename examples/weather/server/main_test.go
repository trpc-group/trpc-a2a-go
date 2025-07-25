package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetWeather 是 getWeather 函数的单元测试
func TestGetWeather(t *testing.T) {
	// 1. 创建一个模拟的 HTTP 服务器 (HTTP Mock Server)
	// httptest.NewServer 会启动一个本地服务器，只在测试期间运行
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 2. 定义服务器的行为
		// 检查请求的 URL 是否是我们预期的
		if r.URL.Path == "/Beijing" {
			// 如果是，就返回一个预设的成功响应
			w.WriteHeader(http.StatusOK) // 设置状态码 200
			fmt.Fprintln(w, "Beijing: ☀️ +25°C") // 写入响应体
		} else {
			// 如果不是，就返回 404 Not Found
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// defer 确保测试结束后，模拟服务器被关闭
	defer server.Close()

	// 3. 运行我们的 getWeather 函数，但把 API 地址指向模拟服务器
	weather, err := getWeather("Beijing", server.URL) // 把模拟服务器的 URL 传进去

	// 4. 断言结果
	if err != nil {
		t.Errorf("getWeather() error = %v, wantErr nil", err)
	}

	expectedWeather := "Beijing: ☀️ +25°C\n" // Fprintln 会加一个换行符
	if weather != expectedWeather {
		t.Errorf("getWeather() = %v, want %v", weather, expectedWeather)
	}
}


package utils

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beck-8/subs-check/config"
	"golang.org/x/net/proxy"
)

const (
	// DefaultTimeout 是HTTP请求的默认超时时间
	DefaultTimeout = 30 * time.Second
)

// ProxyHTTPClient 根据配置创建并返回一个 HTTP 客户端
func ProxyHTTPClient() *http.Client {
	// 创建基本的transport配置
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   DefaultTimeout,
	}

	proxyType := config.GlobalConfig.ProxyType
	proxyAddr := config.GlobalConfig.ProxyUrl

	// 如果没有代理配置，直接返回默认客户端,感觉有点多余
	if proxyType == "" || proxyAddr == "" {
		return client
	}

	// 根据代理类型配置transport
	switch proxyType {
	case "http", "https":
		if !hasScheme(proxyAddr) {
			proxyAddr = "http://" + proxyAddr
		}

		proxyURL, err := url.Parse(proxyAddr)
		if err != nil {
			log.Printf("解析HTTP代理URL失败: %v", err)
			return client
		}
		transport.Proxy = http.ProxyURL(proxyURL)

	case "socks", "socks5":
		if !hasScheme(proxyAddr) {
			proxyAddr = "socks5://" + proxyAddr
		}

		// 解析SOCKS5 URL
		proxyURL, err := url.Parse(proxyAddr)
		if err != nil {
			log.Printf("解析SOCKS5代理URL失败: %v", err)
			return client
		}

		// 提取认证信息
		var auth *proxy.Auth
		if proxyURL.User != nil {
			auth = &proxy.Auth{
				User: proxyURL.User.Username(),
			}
			password, hasPassword := proxyURL.User.Password()
			if hasPassword {
				auth.Password = password
			}
		}

		// 直接使用URL的Host部分
		host := proxyURL.Host

		// 创建SOCKS5连接
		dialer, err := proxy.SOCKS5("tcp", host, auth, proxy.Direct)
		if err != nil {
			log.Printf("创建SOCKS5代理连接失败: %v", err)
			return client
		}

		// 检查是否支持ContextDialer接口
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			// 仅当不支持DialContext时才使用Dial
			log.Printf("警告: SOCKS5代理不支持DialContext，回退到使用已废弃的Dial方法")
			// 创建一个适配器，将Dial转换为DialContext
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}

	default:
		log.Printf("未知的代理类型: %s, 使用直接连接", proxyType)
	}

	return client
}

// hasScheme 检查URL是否包含协议方案
func hasScheme(urlStr string) bool {
	return strings.Contains(urlStr, "://")
}

package check

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/beck-8/subs-check/check/platform"
	"github.com/beck-8/subs-check/config"
	proxyutils "github.com/beck-8/subs-check/proxy"
	"github.com/juju/ratelimit"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/constant"
)

// Result 存储节点检测结果
type Result struct {
	Proxy      map[string]any
	Openai     bool
	Youtube    bool
	Netflix    bool
	Google     bool
	Cloudflare bool
	Disney     bool
	Gemini     bool
	IP         string
	IPRisk     string
	Country    string
}

// ProxyChecker 处理代理检测的主要结构体
type ProxyChecker struct {
	results             []Result
	proxyCount          int
	threadCount         int // 保留用于兼容性，实际使用connectivityThreads和speedTestThreads
	connectivityThreads int
	speedTestThreads    int
	progress            int32
	available           int32
	resultChan          chan Result
	tasks               chan map[string]any // 保留用于兼容性
	connectivityTasks   chan map[string]any
	speedTestTasks      chan map[string]any
}

var Progress atomic.Uint32
var Available atomic.Uint32
var ProxyCount atomic.Uint32
var TotalBytes atomic.Int64

var ForceClose atomic.Bool

var Bucket *ratelimit.Bucket

// NewProxyChecker 创建新的检测器实例
func NewProxyChecker(proxyCount int) *ProxyChecker {
	threadCount := config.GlobalConfig.Concurrent
	if proxyCount < threadCount {
		threadCount = proxyCount
	}

	// 设置连通性测试线程数
	connectivityThreads := config.GlobalConfig.ConnectivityThreads
	if connectivityThreads <= 0 {
		connectivityThreads = config.GlobalConfig.Concurrent
	}
	if proxyCount < connectivityThreads {
		connectivityThreads = proxyCount
	}

	// 设置速度测试线程数
	speedTestThreads := config.GlobalConfig.SpeedTestThreads
	if speedTestThreads <= 0 {
		speedTestThreads = config.GlobalConfig.Concurrent
	}
	if proxyCount < speedTestThreads {
		speedTestThreads = proxyCount
	}

	ProxyCount.Store(uint32(proxyCount))
	return &ProxyChecker{
		results:             make([]Result, 0),
		proxyCount:          proxyCount,
		threadCount:         threadCount, // 保留用于兼容性
		connectivityThreads: connectivityThreads,
		speedTestThreads:    speedTestThreads,
		resultChan:          make(chan Result),
		tasks:               make(chan map[string]any, 1), // 保留用于兼容性
		connectivityTasks:   make(chan map[string]any, proxyCount),
		speedTestTasks:      make(chan map[string]any, proxyCount),
	}
}

// Check 执行代理检测的主函数
func Check() ([]Result, error) {
	proxyutils.ResetRenameCounter()
	ForceClose.Store(false)

	ProxyCount.Store(0)
	Available.Store(0)
	Progress.Store(0)

	TotalBytes.Store(0)

	// 之前好的节点前置
	var proxies []map[string]any
	if config.GlobalConfig.KeepSuccessProxies {
		slog.Info(fmt.Sprintf("添加之前测试成功的节点，数量: %d", len(config.GlobalProxies)))
		proxies = append(proxies, config.GlobalProxies...)
	}
	tmp, err := proxyutils.GetProxies()
	if err != nil {
		return nil, fmt.Errorf("获取节点失败: %w", err)
	}
	proxies = append(proxies, tmp...)
	slog.Info(fmt.Sprintf("获取节点数量: %d", len(proxies)))

	// 重置全局节点
	config.GlobalProxies = make([]map[string]any, 0)

	proxies = proxyutils.DeduplicateProxies(proxies)
	slog.Info(fmt.Sprintf("去重后节点数量: %d", len(proxies)))

	checker := NewProxyChecker(len(proxies))
	return checker.run(proxies)
}

// Run 运行检测流程
func (pc *ProxyChecker) run(proxies []map[string]any) ([]Result, error) {
	if config.GlobalConfig.TotalSpeedLimit != 0 {
		Bucket = ratelimit.NewBucketWithRate(float64(config.GlobalConfig.TotalSpeedLimit*1024*1024), int64(config.GlobalConfig.TotalSpeedLimit*1024*1024/10))
	} else {
		Bucket = ratelimit.NewBucketWithRate(float64(math.MaxInt64), int64(math.MaxInt64))
	}

	slog.Info("开始检测节点")
	slog.Info("当前参数", "timeout", config.GlobalConfig.Timeout, "concurrent", config.GlobalConfig.Concurrent, "connectivity-threads", pc.connectivityThreads, "speed-test-threads", pc.speedTestThreads, "min-speed", config.GlobalConfig.MinSpeed, "download-timeout", config.GlobalConfig.DownloadTimeout, "download-mb", config.GlobalConfig.DownloadMB, "total-speed-limit", config.GlobalConfig.TotalSpeedLimit)

	done := make(chan bool)
	if config.GlobalConfig.PrintProgress {
		go pc.showProgress(done)
	}

	// 第一阶段：连通性测试
	slog.Info("开始连通性测试阶段")
	connectivityPassedProxies := pc.runConnectivityTest(proxies)
	slog.Info(fmt.Sprintf("连通性测试通过节点数量: %d", len(connectivityPassedProxies)))

	// 第二阶段：速度测试
	if len(connectivityPassedProxies) > 0 {
		// 检查是否需要进行速度测试或媒体检测
		needSpeedTest := config.GlobalConfig.SpeedTestUrl != ""
		needMediaCheck := config.GlobalConfig.MediaCheck

		if needSpeedTest || needMediaCheck {
			slog.Info("开始速度测试阶段")
			// 更新进度条的总数为连通性测试通过的节点数
			pc.proxyCount = len(connectivityPassedProxies)
			// 重置进度计数器和可用计数器
			atomic.StoreInt32(&pc.progress, 0)
			atomic.StoreInt32(&pc.available, 0)
			pc.runSpeedTest(connectivityPassedProxies)
		} else {
			slog.Info("跳过速度测试阶段（未配置速度测试URL且未启用媒体检测）")
			// 直接将连通性通过的节点作为最终结果
			for _, proxy := range connectivityPassedProxies {
				result := &Result{Proxy: proxy}
				// 节点重命名处理
				if config.GlobalConfig.RenameNode {
					// 为了保持一致性，在跳过速度测试时也进行完整的重命名
					httpClient := CreateClient(proxy)
					if httpClient != nil {
						country, _ := proxyutils.GetProxyCountry(httpClient.Client)
						result.Proxy["name"] = config.GlobalConfig.NodePrefix + proxyutils.Rename(country)
						httpClient.Close()
					} else {
						// 如果无法创建客户端，则仅添加前缀
						result.Proxy["name"] = config.GlobalConfig.NodePrefix + result.Proxy["name"].(string)
					}
				}
				pc.results = append(pc.results, *result)
				// 🔧 修复：不再重复调用 incrementAvailable()，因为在连通性测试阶段已经计算过了
			}
		}
	} else {
		slog.Warn("没有节点通过连通性测试，跳过速度测试阶段")
	}

	// 等待进度条显示完成
	time.Sleep(100 * time.Millisecond)

	if config.GlobalConfig.PrintProgress {
		done <- true
	}

	if config.GlobalConfig.SuccessLimit > 0 && pc.available >= config.GlobalConfig.SuccessLimit {
		slog.Warn(fmt.Sprintf("达到节点数量限制: %d", config.GlobalConfig.SuccessLimit))
	}
	slog.Info(fmt.Sprintf("可用节点数量: %d", len(pc.results)))
	if config.GlobalConfig.SpeedTestUrl != "" {
		slog.Info(fmt.Sprintf("测速总消耗流量: %.2fGB", float64(TotalBytes.Load())/1024/1024/1024))
	}

	// 检查订阅成功率并发出警告
	pc.checkSubscriptionSuccessRate(proxies)

	return pc.results, nil
}

// worker 处理单个代理检测的工作线程
func (pc *ProxyChecker) worker(wg *sync.WaitGroup) {
	defer wg.Done()
	for proxy := range pc.tasks {
		if result := pc.checkProxy(proxy); result != nil {
			pc.resultChan <- *result
		}
		pc.incrementProgress()
	}
}

// checkConnectivityWithRetry 带重试机制的连通性检测
func (pc *ProxyChecker) checkConnectivityWithRetry(httpClient *ProxyClient, nodeName interface{}) bool {
	retries := config.GlobalConfig.ConnectivityRetries
	if retries <= 0 {
		retries = 1
	}

	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s, 4s...
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			time.Sleep(delay)
			slog.Debug("重试连通性检测", "node", nodeName, "attempt", attempt+1, "total", retries)
		}

		// 使用现有的 platform 检测函数
		if cloudflare, err := platform.CheckCloudflare(httpClient.Client); err != nil || !cloudflare {
			continue
		}

		if google, err := platform.CheckGoogle(httpClient.Client); err != nil || !google {
			continue
		}

		// 检测成功
		if attempt > 0 {
			slog.Debug("连通性检测成功", "node", nodeName, "attempt", attempt+1)
		}
		return true
	}

	slog.Debug("连通性检测失败", "node", nodeName, "retries", retries)
	return false
}

// checkProxy 检测单个代理
func (pc *ProxyChecker) checkProxy(proxy map[string]any) *Result {
	res := &Result{
		Proxy: proxy,
	}

	if os.Getenv("SUB_CHECK_SKIP") != "" {
		// slog.Debug(fmt.Sprintf("跳过检测代理: %v", proxy["name"]))
		return res
	}

	httpClient := CreateClient(proxy)
	if httpClient == nil {
		slog.Debug(fmt.Sprintf("创建代理Client失败: %v", proxy["name"]))
		return nil
	}
	defer httpClient.Close()

	// 使用重试机制进行连通性检测
	if !pc.checkConnectivityWithRetry(httpClient, proxy["name"]) {
		return nil
	}

	var speed int
	var totalBytes int64
	if config.GlobalConfig.SpeedTestUrl != "" {
		var err error
		speed, totalBytes, err = platform.CheckSpeed(httpClient.Client, Bucket)
		TotalBytes.Add(totalBytes)
		if err != nil || speed < config.GlobalConfig.MinSpeed {
			return nil
		}
	}

	if config.GlobalConfig.MediaCheck {
		// 遍历需要检测的平台
		for _, plat := range config.GlobalConfig.Platforms {
			switch plat {
			case "openai":
				if ok, _ := platform.CheckOpenai(httpClient.Client); ok {
					res.Openai = true
				}
			case "youtube":
				if ok, _ := platform.CheckYoutube(httpClient.Client); ok {
					res.Youtube = true
				}
			case "netflix":
				if ok, _ := platform.CheckNetflix(httpClient.Client); ok {
					res.Netflix = true
				}
			case "disney":
				if ok, _ := platform.CheckDisney(httpClient.Client); ok {
					res.Disney = true
				}
			case "gemini":
				if ok, _ := platform.CheckGemini(httpClient.Client); ok {
					res.Gemini = true
				}
			case "iprisk":
				country, ip := proxyutils.GetProxyCountry(httpClient.Client)
				if ip == "" {
					break
				}
				res.IP = ip
				res.Country = country
				risk, err := platform.CheckIPRisk(httpClient.Client, ip)
				if err == nil {
					res.IPRisk = risk
				} else {
					// 失败的可能性高，所以放上日志
					slog.Debug(fmt.Sprintf("查询IP风险失败: %v", err))
				}
			}
		}
	}
	// 更新代理名称
	pc.updateProxyName(res, httpClient, speed)
	pc.incrementAvailable()
	return res
}

// checkConnectivity 检测代理连通性
func (pc *ProxyChecker) checkConnectivity(proxy map[string]any) bool {
	if os.Getenv("SUB_CHECK_SKIP") != "" {
		return true // 跳过模式下认为连通性通过
	}

	httpClient := CreateClient(proxy)
	if httpClient == nil {
		slog.Debug(fmt.Sprintf("创建代理Client失败: %v", proxy["name"]))
		return false
	}
	defer httpClient.Close()

	// 使用重试机制进行连通性检测
	return pc.checkConnectivityWithRetry(httpClient, proxy["name"])
}

// checkSpeed 检测代理速度和其他平台可用性
func (pc *ProxyChecker) checkSpeed(proxy map[string]any) *Result {
	res := &Result{
		Proxy: proxy,
	}

	if os.Getenv("SUB_CHECK_SKIP") != "" {
		return res // 跳过模式下直接返回结果
	}

	httpClient := CreateClient(proxy)
	if httpClient == nil {
		slog.Debug(fmt.Sprintf("创建代理Client失败: %v", proxy["name"]))
		return nil
	}
	defer httpClient.Close()

	var speed int
	var totalBytes int64
	if config.GlobalConfig.SpeedTestUrl != "" {
		var err error
		speed, totalBytes, err = platform.CheckSpeed(httpClient.Client, Bucket)
		TotalBytes.Add(totalBytes)
		if err != nil || speed < config.GlobalConfig.MinSpeed {
			return nil
		}
	}

	if config.GlobalConfig.MediaCheck {
		// 遍历需要检测的平台
		for _, plat := range config.GlobalConfig.Platforms {
			switch plat {
			case "openai":
				if ok, _ := platform.CheckOpenai(httpClient.Client); ok {
					res.Openai = true
				}
			case "youtube":
				if ok, _ := platform.CheckYoutube(httpClient.Client); ok {
					res.Youtube = true
				}
			case "netflix":
				if ok, _ := platform.CheckNetflix(httpClient.Client); ok {
					res.Netflix = true
				}
			case "disney":
				if ok, _ := platform.CheckDisney(httpClient.Client); ok {
					res.Disney = true
				}
			case "gemini":
				if ok, _ := platform.CheckGemini(httpClient.Client); ok {
					res.Gemini = true
				}
			case "iprisk":
				country, ip := proxyutils.GetProxyCountry(httpClient.Client)
				if ip == "" {
					break
				}
				res.IP = ip
				res.Country = country
				risk, err := platform.CheckIPRisk(httpClient.Client, ip)
				if err == nil {
					res.IPRisk = risk
				} else {
					// 失败的可能性高，所以放上日志
					slog.Debug(fmt.Sprintf("查询IP风险失败: %v", err))
				}
			}
		}
	}
	// 更新代理名称
	pc.updateProxyName(res, httpClient, speed)
	return res
}

// updateProxyName 更新代理名称
func (pc *ProxyChecker) updateProxyName(res *Result, httpClient *ProxyClient, speed int) {
	// 以节点IP查询位置重命名节点
	if config.GlobalConfig.RenameNode {
		if res.Country != "" {
			res.Proxy["name"] = config.GlobalConfig.NodePrefix + proxyutils.Rename(res.Country)
		} else {
			country, _ := proxyutils.GetProxyCountry(httpClient.Client)
			res.Proxy["name"] = config.GlobalConfig.NodePrefix + proxyutils.Rename(country)
		}
	}

	name := res.Proxy["name"].(string)
	name = strings.TrimSpace(name)

	var tags []string
	// 获取速度
	if config.GlobalConfig.SpeedTestUrl != "" {
		name = regexp.MustCompile(`\s*\|(?:\s*⬇️\s*[\d.]+[KM]B/s)`).ReplaceAllString(name, "")
		var speedStr string
		if speed < 1024 {
			speedStr = fmt.Sprintf(" ⬇️ %dKB/s", speed)
		} else {
			speedStr = fmt.Sprintf(" ⬇️ %.1fMB/s", float64(speed)/1024)
		}
		tags = append(tags, speedStr)
	}

	if config.GlobalConfig.MediaCheck {
		// 移除已有的标记（IPRisk和平台标记）
		name = regexp.MustCompile(`\s*\|(?:Netflix|Disney|Youtube|Openai|Gemini|\d+%)`).ReplaceAllString(name, "")
	}
	// 添加其他标记
	if res.IPRisk != "" {
		tags = append(tags, res.IPRisk)
	}
	if res.Netflix {
		tags = append(tags, "Netflix")
	}
	if res.Disney {
		tags = append(tags, "Disney")
	}
	if res.Youtube {
		tags = append(tags, "Youtube")
	}
	if res.Openai {
		tags = append(tags, "Openai")
	}
	if res.Gemini {
		tags = append(tags, "Gemini")
	}

	// 将所有标记添加到名称中
	if len(tags) > 0 {
		name += " |" + strings.Join(tags, "|")
	}

	res.Proxy["name"] = name

}

// showProgress 显示进度条
func (pc *ProxyChecker) showProgress(done chan bool) {
	for {
		select {
		case <-done:
			fmt.Println()
			return
		default:
			current := atomic.LoadInt32(&pc.progress)
			available := atomic.LoadInt32(&pc.available)

			if pc.proxyCount == 0 {
				time.Sleep(100 * time.Millisecond)
				break
			}

			// if 0/0 = NaN ,shoule panic
			percent := float64(current) / float64(pc.proxyCount) * 100
			fmt.Printf("\r进度: [%-50s] %.1f%% (%d/%d) 可用: %d",
				strings.Repeat("=", int(percent/2))+">",
				percent,
				current,
				pc.proxyCount,
				available)
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// 辅助方法
func (pc *ProxyChecker) incrementProgress() {
	atomic.AddInt32(&pc.progress, 1)
	Progress.Add(1)
}

func (pc *ProxyChecker) incrementAvailable() {
	atomic.AddInt32(&pc.available, 1)
	Available.Add(1)
}

// distributeProxies 分发代理任务
func (pc *ProxyChecker) distributeProxies(proxies []map[string]any) {
	for _, proxy := range proxies {
		if config.GlobalConfig.SuccessLimit > 0 && atomic.LoadInt32(&pc.available) >= config.GlobalConfig.SuccessLimit {
			break
		}
		if ForceClose.Load() {
			slog.Warn("收到强制关闭信号，停止派发任务")
			break
		}
		pc.tasks <- proxy
	}
	close(pc.tasks)
}

// runConnectivityTest 执行连通性测试
func (pc *ProxyChecker) runConnectivityTest(proxies []map[string]any) []map[string]any {
	var wg sync.WaitGroup
	passedProxies := make([]map[string]any, 0)
	passedProxiesMutex := &sync.Mutex{}

	// 启动连通性测试工作线程
	for i := 0; i < pc.connectivityThreads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for proxy := range pc.connectivityTasks {
				if ForceClose.Load() {
					break
				}

				if passed := pc.checkConnectivity(proxy); passed {
					passedProxiesMutex.Lock()
					passedProxies = append(passedProxies, proxy)
					passedProxiesMutex.Unlock()
					pc.incrementAvailable() // 更新可用节点计数
				}
				pc.incrementProgress()
			}
		}()
	}

	// 分发连通性测试任务
	go func() {
		for _, proxy := range proxies {
			if ForceClose.Load() {
				break
			}
			pc.connectivityTasks <- proxy
		}
		close(pc.connectivityTasks)
	}()

	wg.Wait()
	// 确保可用计数器与实际通过的节点数量一致
	atomic.StoreInt32(&pc.available, int32(len(passedProxies)))
	// 给进度条一点时间显示最终状态
	time.Sleep(200 * time.Millisecond)
	return passedProxies
}

// runSpeedTest 执行速度测试
func (pc *ProxyChecker) runSpeedTest(proxies []map[string]any) {
	var wg sync.WaitGroup
	var collectWg sync.WaitGroup

	// 启动结果收集
	collectWg.Add(1)
	go func() {
		pc.collectResults()
		collectWg.Done()
	}()

	// 启动速度测试工作线程
	for i := 0; i < pc.speedTestThreads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for proxy := range pc.speedTestTasks {
				if ForceClose.Load() {
					break
				}

				if result := pc.checkSpeed(proxy); result != nil {
					pc.resultChan <- *result
				}
				pc.incrementProgress() // 无论成功与否都要更新进度
			}
		}()
	}

	// 分发速度测试任务
	go func() {
		for _, proxy := range proxies {
			if config.GlobalConfig.SuccessLimit > 0 && atomic.LoadInt32(&pc.available) >= config.GlobalConfig.SuccessLimit {
				break
			}
			if ForceClose.Load() {
				break
			}
			pc.speedTestTasks <- proxy
		}
		close(pc.speedTestTasks)
	}()

	wg.Wait()
	close(pc.resultChan)

	// 等待结果收集完成
	collectWg.Wait()
}

// collectResults 收集检测结果
func (pc *ProxyChecker) collectResults() {
	for result := range pc.resultChan {
		pc.results = append(pc.results, result)
		pc.incrementAvailable() // 每收集一个有效结果就更新可用计数
	}
}

// CreateClient creates and returns an http.Client with a Close function
type ProxyClient struct {
	*http.Client
	proxy constant.Proxy
}

func CreateClient(mapping map[string]any) *ProxyClient {
	proxy, err := adapter.ParseProxy(mapping)
	if err != nil {
		slog.Debug(fmt.Sprintf("底层mihomo创建代理Client失败: %v", err))
		return nil
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var u16Port uint16
			if port, err := strconv.ParseUint(port, 10, 16); err == nil {
				u16Port = uint16(port)
			}
			return proxy.DialContext(ctx, &constant.Metadata{
				Host:    host,
				DstPort: u16Port,
			})
		},
		IdleConnTimeout:   time.Duration(config.GlobalConfig.Timeout) * time.Millisecond,
		DisableKeepAlives: true,
	}

	return &ProxyClient{
		Client: &http.Client{
			Timeout:   time.Duration(config.GlobalConfig.Timeout) * time.Millisecond,
			Transport: transport,
		},
		proxy: proxy,
	}
}

// Close closes the proxy client and cleans up resources
// 防止底层库有一些泄露，所以这里手动关闭
func (pc *ProxyClient) Close() {
	if pc.Client != nil {
		pc.Client.CloseIdleConnections()
	}

	// 即使这里不关闭，底层GC的时候也会自动关闭
	if pc.proxy != nil {
		pc.proxy.Close()
	}
	pc.Client = nil
}

// checkSubscriptionSuccessRate 检查订阅成功率并发出警告
func (pc *ProxyChecker) checkSubscriptionSuccessRate(allProxies []map[string]any) {
	// 统计每个订阅的节点总数和成功数
	subStats := make(map[string]struct {
		total   int
		success int
	})

	// 统计所有节点的订阅来源
	for _, proxy := range allProxies {
		if subUrl, ok := proxy["subscription_url"].(string); ok {
			stats := subStats[subUrl]
			stats.total++
			subStats[subUrl] = stats
		}
	}

	// 统计成功节点的订阅来源
	for _, result := range pc.results {
		if result.Proxy != nil {
			if subUrl, ok := result.Proxy["subscription_url"].(string); ok {
				stats := subStats[subUrl]
				stats.success++
				subStats[subUrl] = stats
			}
			delete(result.Proxy, "subscription_url")
		}
	}

	// 检查成功率并发出警告
	for subUrl, stats := range subStats {
		if stats.total > 0 {
			successRate := float32(stats.success) / float32(stats.total)

			// 如果成功率低于x，发出警告
			if successRate < config.GlobalConfig.SuccessRate {
				slog.Warn(fmt.Sprintf("订阅成功率过低: %s", subUrl),
					"总节点数", stats.total,
					"成功节点数", stats.success,
					"成功占比", fmt.Sprintf("%.2f%%", successRate*100))
			} else {
				slog.Debug(fmt.Sprintf("订阅节点统计: %s", subUrl),
					"总节点数", stats.total,
					"成功节点数", stats.success,
					"成功占比", fmt.Sprintf("%.2f%%", successRate*100))
			}
		}
	}
}

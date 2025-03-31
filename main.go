package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly"
	"github.com/gocolly/colly/extensions"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sys/windows"
)

type Spiders struct {
	Url    string
	Host   string
	Links  []string
	Api    []string
	Config *SpiderConfig
}

type SpiderConfig struct {
	Flag        int           // 功能
	Depth       int           // 爬取深度
	Concurrency int           // 并发数
	Cookie      string        // cookie
	Headers     MapFlag       // 自定义header
	Delay       time.Duration // 请求延时
	TriggerWaf  int           // waf处理

	DynamicAllowedDomains int // 是否添加动态爬虫域名
}

func NewSpiderConfig() *SpiderConfig {
	return &SpiderConfig{
		Flag:                  1, //默认使用失效链接扫描,
		Depth:                 3,
		Concurrency:           3,               // 默认并发数
		Delay:                 1 * time.Second, // 默认延迟
		DynamicAllowedDomains: 1,               // 默认动态域名
		TriggerWaf:            1,               //默认触发WAF
	}
}

// MapFlag 自定义类型实现 flag.Value 接口,实现自定义header
type MapFlag map[string]string

func (m *MapFlag) String() string {
	return fmt.Sprintf("%v", *m)
}

func (m *MapFlag) Set(value string) error {
	if *m == nil {
		*m = make(MapFlag)
	}

	pairs := strings.Split(value, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf(" invalid pair: %s", pair)
		}
		(*m)[kv[0]] = kv[1]
	}
	return nil
}

// 403计数
var (
	errorCount int        // 错误计数器
	errorUrl   string     //错误页面
	mu         sync.Mutex // 互斥锁（用于并发安全）
)

// 正则
var (
	jsFileRegex  = regexp.MustCompile(`\.js`)
	pdfFileRegex = regexp.MustCompile(`\.pdf`)
	urlRegex     = regexp.MustCompile(`https?://[^\s"'()]+`)
	apiRegex     = regexp.MustCompile(`'(\S*\?\S*[^'])'`)
)

// 创建爬虫
func (s *Spiders) createCollector(host string, depth int, parallelism int, delay time.Duration) *colly.Collector {

	c := colly.NewCollector(
		colly.IgnoreRobotsTxt(),
		colly.AllowedDomains(host), // 允许域名
		colly.MaxDepth(depth),      // 爬取深度
	)
	//设定cookie
	if s.Config.Cookie != "" {
		cookie, _ := parseCookieString(s.Config.Cookie)
		c.SetCookies(host, cookie)
	}

	c.SetRequestTimeout(12 * time.Second) // 全局超时时间

	c.Limit(&colly.LimitRule{
		DomainGlob:  host,
		Parallelism: parallelism, // 并发数
		RandomDelay: delay,       // 随机请求间隔
	})
	extensions.RandomUserAgent(c)
	return c
}

// cookie解析
func parseCookieString(cookieStr string) ([]*http.Cookie, error) {
	rawCookies := cookieStr
	header := http.Header{}
	header.Add("Cookie", rawCookies)
	request := http.Request{Header: header}
	return request.Cookies(), nil
}

// 公共http请求组件
func commonHTTPRequest(targetURL string) (*http.Response, error) {

	client := &http.Client{
		Timeout: 15 * time.Second, // 设置超时时间为 10 秒
	}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4471.124 Safari/537.36")

	return client.Do(req)
}

// 爬虫
// flag 0 API 扫描
// flag 1 失效链接
func (s *Spiders) Crawler() (api []string, links []string) {

	//返回值
	m := make(map[string]int)
	//URL解析
	parsedURL, err := url.Parse(s.Url)
	if err != nil {
		fmt.Println(" URL 解析错误:", err)
		return
	}
	//测试连通性
	_, err = commonHTTPRequest(s.Url)
	if err != nil {
		fmt.Println(" 连接失败,请检查输入链接:", err)
		return
	}
	// 提取 host
	s.Host = parsedURL.Host
	// 提取协议部分（basehttp）
	basehttp := parsedURL.Scheme + "://"

	if s.Config.Flag == 0 {
		// colly Collector创建
		delay := 1500 * time.Millisecond
		api_c := s.createCollector(string(s.Host), 10, 10, delay)
		// 生命周期
		// 请求前
		api_c.OnRequest(func(r *colly.Request) {
			// fmt.Println("爬取中……", r.URL)
		})
		// 响应后,从js文件处理
		api_c.OnResponse(func(r *colly.Response) {
			if jsFileRegex.MatchString(r.Request.URL.String()) {
				fmt.Println("js文件处理位置: " + r.Request.URL.String())
			}
		})
		// 选择器
		api_c.OnHTML("*", func(e *colly.HTMLElement) {
			e.Request.Visit(e.Attr("href"))
			e.Request.Visit(e.Attr("src"))
			if e.Name == "script" {
				if apiRegex.FindStringSubmatch(e.Text) != nil {
					for _, v := range apiRegex.FindAllStringSubmatch(e.Text, -1) {
						m[v[1]] = 1
					}
				}
			}
		})
		// 回调完成后调用
		api_c.OnScraped(func(r *colly.Response) {
			s.Links = append(s.Links, r.Request.URL.String())
		})
		// 错误信息
		api_c.OnError(func(_ *colly.Response, err error) {
			fmt.Println(" Something went wrong:", err)
		})
		// 访问
		api_c.Visit(s.Url)
		//循环遍历m
		for k := range m {
			s.Api = append(s.Api, basehttp+s.Host+k)
		}
	}
	if s.Config.Flag == 1 {
		// 失效链接处理
		//获取waf404页面
		var WafBody []byte
		if s.Config.TriggerWaf == 1 {
			req, err := commonHTTPRequest(basehttp + s.Host + "/page?param=<script>alert(1)</script>")
			if err != nil {
				{
				} // fmt.Println(" 请求失败:", err)
			}
			// 确保 req 不为 nil 后再执行 defer 和读取 Body

			if req != nil {
				defer req.Body.Close()
				WafBody, err = io.ReadAll(req.Body)
				if err != nil {
					{
					} // fmt.Println(" 读取 404PageBody 失败:", err)
				}
			}
		}

		var wj = true
		// colly Collector创建

		invalid_url_c := s.createCollector(string(s.Host), s.Config.Depth, s.Config.Concurrency, s.Config.Delay)
		//生命周期
		invalid_url_c.OnRequest(func(r *colly.Request) {
			// 自定义header
			if s.Config.Headers != nil {
				for k, v := range s.Config.Headers {
					r.Headers.Set(k, v)
				}

			}

			// 输出到固定位置
			fmt.Printf("\x1b[2;1H") // 光标
			if wj {
				fmt.Print("\x1b[K \x1b[38;5;205;48;5;234mProgress: ..\x1b[0m")
				wj = !wj
			} else {
				fmt.Print("\x1b[K \x1b[38;5;205;48;5;234mProgress: ...\x1b[0m")
				wj = !wj
			}

			fmt.Printf("\x1b[6;1H") // 光标
			fmt.Printf("\x1b[K \x1b[32;47m正在爬取: %s\x1b[0m", r.URL)

		})
		//响应
		invalid_url_c.OnResponse(func(r *colly.Response) {
			if string(WafBody) != "" && r.StatusCode == 200 {
				if CosineSimilar([]rune(string(WafBody)), []rune(string(r.Body))) >= 0.99 {
					s.Links = append(s.Links, r.Request.URL.String())
				}
			}
			// 处理js里面的url

			if jsFileRegex.MatchString(r.Request.URL.String()) {
				s.jsORsj(string(r.Body), r.Request.URL.String())
			}
		})

		// 选择器
		invalid_url_c.OnHTML("*", func(e *colly.HTMLElement) {
			// 处理 href 属性
			if href := e.Attr("href"); href != "" {
				if s.Config.DynamicAllowedDomains == 1 {
					s.handleLink(invalid_url_c, href)
				}
				if !pdfFileRegex.MatchString(href) {
					e.Request.Visit(href)
				}

			}

			// 处理 src 属性
			if src := e.Attr("src"); src != "" {
				if s.Config.DynamicAllowedDomains == 1 {
					s.handleLink(invalid_url_c, src)
				}
				if !pdfFileRegex.MatchString(src) {
					e.Request.Visit(src)
				}
			}
			// 处理<cript>标签里的url
			if e.Name == "script" {
				if urlRegex.FindStringSubmatch(e.Text) != nil {
					s.jsORsj(e.Text, e.Request.URL.String())
				}
			}
		})
		// 回调完成后调用
		invalid_url_c.OnScraped(func(r *colly.Response) {
		})
		// 错误信息
		invalid_url_c.OnError(func(r *colly.Response, err error) {
			if err.Error() != "" {
				if r != nil && r.StatusCode == 403 {
					mu.Lock()         // 加锁
					defer mu.Unlock() // 解锁
					errorCount++
					if errorCount == 1 {
						errorUrl = r.Request.URL.String()
					}
					if errorCount >= 10 {
						req, _ := commonHTTPRequest(s.Url)
						if req != nil && req.StatusCode == 403 {
							fmt.Printf("\x1b[8;1H") // 光标
							fmt.Println("\x1b[K \x1b[32;47m ip可能已被封禁,请手动检测！\x1b[0m" + "第一次403Url:" + errorUrl)
							os.Exit(0)
						} else if req != nil && req.StatusCode == 200 {
							errorCount = 0
						}
					}
				}
				s.Links = append(s.Links, r.Request.Headers.Get("Referer")+"  ==>  "+r.Request.URL.String())
			}
		})
		// 访问
		invalid_url_c.Visit(s.Url)
	}
	return s.Api, s.Links
}

// js与script内容处理
func (s *Spiders) jsORsj(x string, frontUrl string) {
	for _, url := range urlRegex.FindAllString(x, -1) {
		req, errs := commonHTTPRequest(url)
		if errs != nil || req == nil {
			s.Links = append(s.Links, frontUrl+"  ==>  "+url)
			continue
		}
		defer func() {
			if req.Body != nil {
				req.Body.Close()
			}
		}()

		switch {
		case req.StatusCode == 403:
			// 单独处理 403 错误
			if strings.HasSuffix(url, ".html") {
				s.Links = append(s.Links, frontUrl+"  ==>  "+url)
			}
		case req.StatusCode >= 400 && req.StatusCode < 600:
			// 处理其他4xx、5xx错误
			s.Links = append(s.Links, frontUrl+"  ==>  "+url)
		}
	}
}

// 处理链接，动态添加子域名
func (s *Spiders) handleLink(c *colly.Collector, link string) {
	parsedURL, err := url.Parse(link)
	if err != nil {
		return
	}
	domain := parsedURL.Hostname()
	if domain == "" {
		return
	}
	// 获取输入一级域名
	TLDPlusOne, _ := publicsuffix.EffectiveTLDPlusOne(s.Host) //获取域名

	// 检查是否是 example.com 的子域名
	if strings.HasSuffix(domain, "."+TLDPlusOne) || domain == TLDPlusOne {
		// 如果域名尚未在 AllowedDomains 中，则动态添加
		if !contains(c.AllowedDomains, domain) {

			c.AllowedDomains = append(c.AllowedDomains, domain)

			fmt.Printf("\x1b[4;1H") // 光标
			fmt.Printf("\x1b[K \x1b[38;5;205;48;5;234mAdded domain to allowed list: %s\x1b[0m", domain)
		}
	}
}

// 辅助函数：检查切片中是否包含某个字符串
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// 余弦相似度算法
func CosineSimilar(srcWords, dstWords []rune) float64 {
	// get all words
	allWordsMap := make(map[rune]int, 0)
	for _, word := range srcWords {
		if _, found := allWordsMap[word]; !found {
			allWordsMap[word] = 1
		} else {
			allWordsMap[word] += 1
		}
	}
	for _, word := range dstWords {
		if _, found := allWordsMap[word]; !found {
			allWordsMap[word] = 1
		} else {
			allWordsMap[word] += 1
		}
	}

	// stable the sort
	allWordsSlice := make([]rune, 0)
	for word := range allWordsMap {
		allWordsSlice = append(allWordsSlice, word)
	}

	// assemble vector
	srcVector := make([]int, len(allWordsSlice))
	dstVector := make([]int, len(allWordsSlice))
	for _, word := range srcWords {
		if index := indexOfSclie(allWordsSlice, word); index != -1 {
			srcVector[index] += 1
		}
	}
	for _, word := range dstWords {
		if index := indexOfSclie(allWordsSlice, word); index != -1 {
			dstVector[index] += 1
		}
	}

	// calc cos
	numerator := float64(0)
	srcSq := 0
	dstSq := 0
	for i, srcCount := range srcVector {
		dstCount := dstVector[i]
		numerator += float64(srcCount * dstCount)
		srcSq += srcCount * srcCount
		dstSq += dstCount * dstCount
	}
	denominator := math.Sqrt(float64(srcSq * dstSq))

	return numerator / denominator
}

// 根据值从slice取索引
func indexOfSclie(slice []rune, item rune) int {

	for index, value := range slice {
		if value == item {
			return index
		}
	}
	return -1
}

func spider(url string, config SpiderConfig) {
	a := Spiders{Url: url, Config: &config}

	fmt.Printf("\x1b[2J")

	_, links := a.Crawler()
	fmt.Println()
	fmt.Println()
	fmt.Println(" \x1b[38;5;205;48;5;234m无效链接: \x1b[0m")
	fmt.Println()

	for _, v := range links {
		fmt.Printf(" \x1b[38;5;205;48;5;234m%s\r\n\x1b[0m", v)
	}
}

func init() {
	// 启用 Windows 的 VT100 支持
	stdout := windows.Handle(os.Stdout.Fd())
	var mode uint32
	windows.GetConsoleMode(stdout, &mode)
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING // 使用预定义的常量
	windows.SetConsoleMode(stdout, mode)
}

func main() {

	config := NewSpiderConfig()

	// 定义命令行参数，并设置默认值和提示信息
	url := flag.String("u", "", "URL to process")
	flag.IntVar(&config.Flag, "f", config.Flag, "Function to execute (1 for default -失效链接)")
	flag.IntVar(&config.Depth, "d", config.Depth, "Depth of processing")
	flag.IntVar(&config.Concurrency, "p", config.Concurrency, "Number of concurrent processes")
	flag.StringVar(&config.Cookie, "c", config.Cookie, "Cookie for authentication")
	flag.IntVar(&config.TriggerWaf, "thw", config.TriggerWaf, "Trigger The Waf (1 for Trigger)")

	// 注册自定义 flag
	flag.Var(&config.Headers, "h", "Headers in key=value format, comma separated")

	// 解析命令行参数
	flag.Parse()
	if *url == "" {
		fmt.Printf(" Wrong URL,maby u need \"--help\" !")
		return
	}

	spider(*url, *config)

}

package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly"
	"github.com/gocolly/colly/extensions"
	"golang.org/x/net/publicsuffix"
)

type Spiders struct {
	Url    string
	Host   string
	Cookie string
	Links  []string
	Api    []string
}

// 是否添加动态爬虫域名（爬取同顶级域名下子域内容）,默认为开启
var DynamicAllowedDomains = 1

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
	if s.Cookie != "" {
		cookie, _ := parseCookieString(s.Cookie)
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
func (s *Spiders) Crawler(flag int, depth int, parallelism int) (api []string, links []string) {

	//返回值
	m := make(map[string]int)
	//URL解析
	parsedURL, err := url.Parse(s.Url)
	if err != nil {
		fmt.Println("URL 解析错误:", err)
		return
	}
	//测试连通性
	_, err = commonHTTPRequest(s.Url)
	if err != nil {
		fmt.Println("连接失败,请检查输入链接:", err)
		return
	}
	// 提取 host
	s.Host = parsedURL.Host
	// 提取协议部分（basehttp）
	basehttp := parsedURL.Scheme + "://"

	if flag == 0 {
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
			fmt.Println("Something went wrong:", err)
		})
		// 访问
		api_c.Visit(s.Url)
		//循环遍历m
		for k := range m {
			s.Api = append(s.Api, basehttp+s.Host+k)
		}
	}
	if flag == 1 {
		// 失效链接处理
		//获取waf404页面
		req, err := commonHTTPRequest(basehttp + s.Host + "/page?param=<script>alert(1)</script>")
		if err != nil {
			fmt.Println("请求失败:", err)
		}
		// 确保 req 不为 nil 后再执行 defer 和读取 Body
		var body []byte
		if req != nil {
			defer req.Body.Close()
			body, err = io.ReadAll(req.Body)
			if err != nil {
				fmt.Println("读取 404PageBody 失败:", err)
			}
		}
		// colly Collector创建
		delay := 100 * time.Millisecond
		invalid_url_c := s.createCollector(string(s.Host), depth, parallelism, delay)
		//生命周期
		invalid_url_c.OnRequest(func(r *colly.Request) {
			fmt.Println("爬取中……", r.URL)
		})
		//响应
		invalid_url_c.OnResponse(func(r *colly.Response) {
			if string(body) != "" && r.StatusCode == 200 {
				if CosineSimilar([]rune(string(body)), []rune(string(r.Body))) >= 0.99 {
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
				if DynamicAllowedDomains == 1 {
					s.handleLink(invalid_url_c, href)
				}
				if !pdfFileRegex.MatchString(href) {
					e.Request.Visit(href)
				}

			}

			// 处理 src 属性
			if src := e.Attr("src"); src != "" {
				if DynamicAllowedDomains == 1 {
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
					if errorCount >= 3 {
						req, _ := commonHTTPRequest(s.Url)
						if req != nil && req.StatusCode == 403 {
							fmt.Println("ip可能已被封禁,请手动检测！" + "第一次403Url:" + errorUrl)
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
			println("Added domain to allowed list:", domain)
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

func spider(url string, flag int, dep int, pll int, extras ...string) {
	a := Spiders{Url: url}
	// 检查是否有传递 cookie
	if len(extras) > 0 && extras[0] != "" {
		a.Cookie = extras[0] //设定cookie
	}
	_, links := a.Crawler(flag, dep, pll)
	fmt.Print("无效链接：\r\n")

	for _, v := range links {
		fmt.Print(v, "\r\n")
	}

}

func main() {

	// 定义命令行参数，并设置默认值和提示信息
	url := flag.String("u", "", "URL to process")
	function := flag.Int("f", 1, "Function to execute (1 for default -失效链接)")
	depth := flag.Int("d", 3, "Depth of processing")
	concurrency := flag.Int("p", 3, "Number of concurrent processes")
	cookie := flag.String("c", "", "Cookie for authentication")

	// 解析命令行参数
	flag.Parse()
	if *url == "" {
		fmt.Printf("Wrong URL,maby u need \"--help\" !")
		return
	}

	spider(*url, *function, *depth, *concurrency, *cookie)

}

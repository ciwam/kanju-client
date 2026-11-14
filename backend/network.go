package backend

// network.go — 出口网络区域检测。
//
// 背景：影视资源站（如量子资源 v.cdnlz21.com）的播放 CDN 要求中国大陆 IP，
// 海外出口时返回 404 页「请使用国内网络访问此页面」，前端只报「视频加载失败」，
// 用户无法自行定位。此 API 供前端登录后检测一次，海外出口时弹窗提示。
//
// 检测设计（重要）：
//   1. 主判定 = CDN geo 探测：请求量子资源 CDN 的固定假资源路径，检测响应
//      是否为 geo 拦截页（特征文案「请使用国内网络」）。geo 拦截在 CDN 边缘、
//      路由匹配之前生效，假路径即可触发（已实测），且与播放解析 100% 同路径。
//      —— 为什么不用第三方 IP 归属地服务做主判定：用户常开按规则分流的代理，
//      「到 ip-api.com 的出口」是直连（国内），「到资源站 CDN 的出口」是海外，
//      两者不同，IP 服务结论会误导（实测踩过）。
//   2. 辅助信息 = IP 归属地（ip-api.com 主 / cip.cc 备），仅用于弹窗展示出口
//      国家与 IP；CDN 探测网络失败时才降级为判定依据。
//
// 结果 region：cn（可播）/ overseas（弹窗）/ unknown（双通道失败，前端不弹窗，
// 宁可少弹不误报）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// CDN geo 探测超时（主判定，直接决定弹窗与否，给足时间）
	regionProbeTimeout = 5 * time.Second
	// IP 归属地查询超时（仅展示信息，海外出口下 ip-api 常走远路，不宜久等）
	regionIPTmo    = 3500 * time.Millisecond
	regionCacheTTL = 10 * time.Minute
	// geoProbeURL 是量子资源 CDN 的固定假资源路径：geo 拦截层对任何
	// /share/<32hex> 路径在路由前生效（实测），无需真实 hash，恒定可用。
	geoProbeURL  = "https://v.cdnlz21.com/share/00000000000000000000000000000000"
	geoBlockMark = "请使用国内网络"
)

// regionCache 缓存检测结果：前端每次进入应用都会调一次，
// 上游限速 + 延迟都要求本地缓存。
var regionCache struct {
	sync.Mutex
	data      map[string]any
	expiresAt time.Time
}

type regionResult struct {
	Region       string `json:"region"`      // cn | overseas | unknown
	CountryCode  string `json:"countryCode"` // US / CN / ...（IP 服务提供，尽力而为）
	Country      string `json:"country"`     // 中文国家/地区名（尽力而为）
	IP           string `json:"ip"`
	Provider     string `json:"provider"`     // 主判定来源：cdn-geo-probe / ip-api.com / cip.cc / none
	CDNReachable bool   `json:"cdnReachable"` // 资源站 CDN 是否可达（geo 探测成功时才有意义）
}

// regionHTTPRequest 用服务端直连视角发起 GET（复用 ssrfSafeTransport：
// 不走 HTTP_PROXY，与播放解析的网络路径一致），返回响应 body。
// timeout 传 0 时用主探测超时。
func regionHTTPRequest(url string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = regionProbeTimeout
	}
	client := &http.Client{Transport: ssrfSafeTransport, Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", desktopUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// geo 拦截页 / 404 / m3u8 都是小响应体，64KB 足够
	buf := make([]byte, 0, 2048)
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil || len(buf) > 64*1024 {
			break
		}
	}
	return string(buf), nil
}

// probeCDNGeo 主判定：探测量子资源 CDN 的 geo 拦截行为。
// 返回 (资源站是否可达, 探测是否成功)。
func probeCDNGeo() (reachable bool, ok bool) {
	body, err := regionHTTPRequest(geoProbeURL, regionProbeTimeout)
	if err != nil {
		// 网络不可达（DNS/超时）：无法判定，不误报
		logWarn("cdn geo probe: %v", err)
		return false, false
	}
	// 海外出口：CDN 边缘返回 geo 拦截页（含特征文案，HTTP 404）
	if strings.Contains(body, geoBlockMark) {
		return false, true
	}
	// 国内出口：请求穿过 geo 层，命中路由后的普通 404（无特征文案）
	return true, true
}

// probeViaIPAPI IP 归属地主通道：ip-api.com 免费端点（无 key，中文国家名）。
func probeViaIPAPI() (regionResult, error) {
	body, err := regionHTTPRequest("http://ip-api.com/json/?fields=status,message,country,countryCode,query&lang=zh-CN", regionIPTmo)
	if err != nil {
		return regionResult{}, err
	}
	var m struct {
		Status      string `json:"status"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return regionResult{}, err
	}
	if m.Status != "success" {
		return regionResult{}, fmt.Errorf("ip-api status=%s", m.Status)
	}
	return regionResult{
		Region:      regionOf(m.CountryCode),
		CountryCode: m.CountryCode,
		Country:     m.Country,
		IP:          m.Query,
		Provider:    "ip-api.com",
	}, nil
}

// probeViaCipCC IP 归属地备通道：cip.cc 文本格式（国内可达性好）。
// 行格式为「IP\t: x.x.x.x」「地址\t: 中国  北京  北京」，按冒号切分取值。
func probeViaCipCC() (regionResult, error) {
	body, err := regionHTTPRequest("http://cip.cc", regionIPTmo)
	if err != nil {
		return regionResult{}, err
	}
	var ip, addr string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "IP"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				ip = strings.TrimSpace(v)
			}
		case strings.HasPrefix(line, "地址"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				addr = strings.TrimSpace(v)
			}
		}
	}
	if ip == "" && addr == "" {
		return regionResult{}, fmt.Errorf("cip.cc unparsable")
	}
	country := addr
	if fields := strings.Fields(addr); len(fields) > 0 {
		country = fields[0]
	}
	region := "overseas"
	// 「中国 香港 / 台湾 / 澳门」对量子 CDN 的策略与大陆一致可访问，保守算 cn
	if strings.HasPrefix(country, "中国") {
		region = "cn"
	}
	return regionResult{Region: region, Country: country, IP: ip, Provider: "cip.cc"}, nil
}

func regionOf(countryCode string) string {
	if strings.EqualFold(strings.TrimSpace(countryCode), "CN") {
		return "cn"
	}
	return "overseas"
}

// cachedRegionResult 带缓存的检测结果：TTL 内直接复用。
func cachedRegionResult(refresh bool) regionResult {
	regionCache.Lock()
	if !refresh && regionCache.data != nil && time.Now().Before(regionCache.expiresAt) {
		out := regionResult{
			Region:       regionCache.data["region"].(string),
			CountryCode:  regionCache.data["countryCode"].(string),
			Country:      regionCache.data["country"].(string),
			IP:           regionCache.data["ip"].(string),
			Provider:     regionCache.data["provider"].(string),
			CDNReachable: regionCache.data["cdnReachable"].(bool),
		}
		regionCache.Unlock()
		return out
	}
	regionCache.Unlock()

	res := regionResult{Region: "unknown", Provider: "none"}

	// 1) 主判定：CDN geo 探测（锁外进行，避免慢上游阻塞并发请求）
	if reachable, ok := probeCDNGeo(); ok {
		res.Region = map[bool]string{true: "cn", false: "overseas"}[reachable]
		res.CDNReachable = reachable
		res.Provider = "cdn-geo-probe"
	}

	// 2) IP 归属地：展示信息；主判定失败时降级为判定依据
	var info regionResult
	var infoErr error
	if info, infoErr = probeViaIPAPI(); infoErr != nil {
		logWarn("network region via ip-api: %v", infoErr)
		if info, infoErr = probeViaCipCC(); infoErr != nil {
			logWarn("network region via cip.cc: %v", infoErr)
		}
	}
	if infoErr == nil {
		res.IP = info.IP
		res.Country = info.Country
		res.CountryCode = info.CountryCode
		if res.Region == "unknown" {
			// CDN 探测失败（如 DNS 被代理环境干扰），降级用 IP 归属地判定
			res.Region = info.Region
			res.Provider = info.Provider
			res.CDNReachable = info.Region == "cn"
		}
	}

	regionCache.Lock()
	regionCache.data = map[string]any{
		"region": res.Region, "countryCode": res.CountryCode,
		"country": res.Country, "ip": res.IP, "provider": res.Provider,
		"cdnReachable": res.CDNReachable,
	}
	regionCache.expiresAt = time.Now().Add(regionCacheTTL)
	regionCache.Unlock()
	return res
}

// handleNetworkRegion GET /api/network/region[?refresh=1]
// 返回服务端（= 用户本机）网络出口对资源站 CDN 的可达区域，供前端弹窗使用。
func (s *Server) handleNetworkRegion(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	res := cachedRegionResult(refresh)
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": res})
}

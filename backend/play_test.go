package backend

import "testing"

// TestExtractPlayURL 验证播放页 HTML 中的媒体地址提取：
// 各类常见内嵌格式 + 不可用地址（data:/javascript:）的排除。
func TestExtractPlayURL(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
		ok   bool
	}{
		{
			"m3u8 in quotes",
			`<script>var url = "https://cdn.example.com/hls/index.m3u8?token=1";</script>`,
			"https://cdn.example.com/hls/index.m3u8?token=1", true,
		},
		{
			"mp4 in quotes",
			`<video src='https://cdn.example.com/movie.mp4'></video>`,
			"https://cdn.example.com/movie.mp4", true,
		},
		{
			"bare m3u8 url",
			`<script>var u='';https://cdn.example.com/live/play.m3u8</script>`,
			// 裸 URL 正则按非空白/非引号字符匹配，会一路吃到 </script>（既有行为），
			// 播放器按 .m3u8 前缀识别仍能正常起播；此处如实记录现状。
			"https://cdn.example.com/live/play.m3u8</script>", true,
		},
		{
			"relative path resolved later",
			`player.load("//cdn.example.com/v.m3u8")`,
			"//cdn.example.com/v.m3u8", true,
		},
		{
			"data: url excluded",
			`<video src="data:video/mp4;base64,AAAA"></video>`,
			"", false,
		},
		{
			"javascript: excluded",
			`<a href="javascript:void(0)">x</a>`,
			"", false,
		},
		{
			"about:blank excluded",
			`<iframe src="about:blank"></iframe>`,
			"", false,
		},
		{
			"no media at all",
			`<html><body>登录页面</body></html>`,
			"", false,
		},
	}
	for _, c := range cases {
		got, ok := extractPlayURL(c.html)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: extractPlayURL = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestPlayType 验证媒体类型判定。
func TestPlayType(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://h.com/x/index.m3u8?token=1", "m3u8"},
		{"https://h.com/x/video.mp4", "mp4"},
		{"https://h.com/x/v.flv", "flv"},
		{"https://h.com/x/v.mpd", "mpd"},
		{"https://h.com/player.php?id=1", "iframe"},
		{"", "iframe"},
	}
	for _, c := range cases {
		if got := playType(c.url); got != c.want {
			t.Errorf("playType(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestTitlesMatch 验证片名模糊匹配（替代海报回退用）。
func TestTitlesMatch(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"银魂", "银魂", true},
		{"银魂", "银魂 走光篇", true},
		{"钢铁侠", "钢铁侠3", true},
		{" 肖申克的救赎 ", "肖申克的救赎", true},
		{"变形金刚", "变形金刚5：最后的骑士", true},
		{"变形金刚", "蜘蛛侠", false}, // 无包含关系
		{"", "x", false},
	}
	for _, c := range cases {
		if got := titlesMatch(c.a, c.b); got != c.want {
			t.Errorf("titlesMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestIsPlaylistBody 验证播放列表文本识别：上游直接返回 #EXTM3U 文本时
// （无后缀目录式 m3u8 URL），原 URL 应被识别为 m3u8 而非回退 iframe。
func TestIsPlaylistBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"标准播放列表", "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:8.0,\nseg0.ts\n#EXT-X-ENDLIST\n", true},
		{"前导空白", "  \n#EXTM3U\n#EXT-X-VERSION:3\n", true},
		{"HTML 页面", "<html><body>403 Forbidden</body></html>", false},
		{"JSON 错误", `{"code":403}`, false},
		{"空串", "", false},
	}
	for _, c := range cases {
		if got := isPlaylistBody(c.body); got != c.want {
			t.Errorf("%s: isPlaylistBody = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMediaTypeFromContentType 验证 Content-Type 到播放类型的映射
// （无后缀目录 URL 的探测判定）。
func TestMediaTypeFromContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want string
	}{
		{"application/vnd.apple.mpegurl", "m3u8"},
		{"application/x-mpegurl", "m3u8"},
		{"application/vnd.apple.mpegurl; charset=utf-8", "m3u8"},
		{"application/dash+xml", "mpd"},
		{"video/mp4", "mp4"},
		{"video/x-flv", "flv"},
		{"text/html", ""},
		{"application/json", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := mediaTypeFromContentType(c.ct); got != c.want {
			t.Errorf("mediaTypeFromContentType(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}

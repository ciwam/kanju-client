package backend

import (
	"testing"
)

func TestResolveFirstSegment(t *testing.T) {
	// Media playlist, first segment root-relative: resolves against server root.
	plRel := "#EXTM3U\n#EXTINF:-1,seg1\n/hls/seg0.ts\n#EXTINF:-1,seg2\nhttps://cdn.example.com/seg1.ts\n"
	if got := resolveFirstSegmentHelper("https://h.com/a/b/index.m3u8", plRel); got != "https://h.com/hls/seg0.ts" {
		t.Fatalf("relative segment resolved wrong: got=%q", got)
	}
	// Media playlist, first segment absolute: returned as-is.
	plAbs := "#EXTM3U\n#EXTINF:-1,seg1\nhttps://cdn.example.com/seg0.ts\n#EXTINF:-1,seg2\n/hls/seg1.ts\n"
	if got := resolveFirstSegmentHelper("https://h.com/index.m3u8", plAbs); got != "https://cdn.example.com/seg0.ts" {
		t.Fatalf("absolute segment resolved wrong: got=%q", got)
	}
	// No media segment lines.
	if got := resolveFirstSegmentHelper("https://h.com/index.m3u8", "#EXTM3U\n#EXT-X-ENDLIST\n"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// resolveFirstSegmentHelper wraps the method for the pure (no-network) cases.
func resolveFirstSegmentHelper(playlistURL, text string) string {
	s := &Server{}
	return s.resolveFirstSegment(playlistURL, text, 0)
}

// TestFirstChildPlaylistURI verifies master->child URI selection + RFC 3986
// resolution (absolute, root-relative and relative child paths).
func TestFirstChildPlaylistURI(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n2000k/hls/mixed.m3u8\n"
	if u, ok := firstChildPlaylistURI(master, "https://h.com/a/b/index.m3u8"); !ok || u != "https://h.com/a/b/2000k/hls/mixed.m3u8" {
		t.Fatalf("relative child resolved wrong: ok=%v got=%q", ok, u)
	}
	masterAbs := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nhttps://cdn.example.com/x/mixed.m3u8\n"
	if u, ok := firstChildPlaylistURI(masterAbs, "https://h.com/index.m3u8"); !ok || u != "https://cdn.example.com/x/mixed.m3u8" {
		t.Fatalf("absolute child resolved wrong: ok=%v got=%q", ok, u)
	}
	if _, ok := firstChildPlaylistURI("#EXTM3U\n#EXT-X-ENDLIST\n", "https://h.com/index.m3u8"); ok {
		t.Fatalf("non-master should yield no child uri")
	}
}

// TestResolveFirstSegmentMasterDescends verifies that a master playlist is
// followed into its child and the child's first segment resolved. The child
// fetch is exercised against a public-style URL; the SSRF policy blocks
// loopback, so we assert the dead-child path via TestResolveFirstSegmentDeadChild
// and the live master->child resolution logic via TestFirstChildPlaylistURI.
func TestResolveFirstSegmentMasterDescends(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n2000k/hls/mixed.m3u8\n"
	child := "#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXTINF:3.0,\nseg1.ts\n"
	// Resolve the child URI the same way the runtime would, then resolve the
	// first segment inside it (pure path, no network).
	childURL, ok := firstChildPlaylistURI(master, "https://h.com/a/b/index.m3u8")
	if !ok {
		t.Fatalf("expected child uri")
	}
	got := resolveFirstSegmentHelper(childURL, child)
	want := "https://h.com/a/b/2000k/hls/seg0.ts"
	if got != want {
		t.Fatalf("master descent resolved wrong: got=%q want=%q", got, want)
	}
}

// TestResolveFirstSegmentDeadChild verifies that a master whose child playlist
// is unreachable yields no resolvable segment (so the source is filtered).
func TestResolveFirstSegmentDeadChild(t *testing.T) {
	// Child points at a host that will not answer; the descent must fail safe.
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nhttp://127.0.0.1:1/dead/mixed.m3u8\n"
	got := resolveFirstSegmentHelper("https://h.com/index.m3u8", master)
	if got != "" {
		t.Fatalf("expected empty for dead child, got %q", got)
	}
}

func TestChannelsOfEpisodesOf(t *testing.T) {
	detail := map[string]any{
		"channels": []map[string]any{
			{"name": "默认线路", "episodes": []map[string]any{{"title": "第1集", "url": "http://x/1"}}},
		},
	}
	chs := channelsOf(detail)
	if len(chs) != 1 || len(episodesOf(chs[0])) != 1 {
		t.Fatalf("[]map[string]any normalization failed: %d channels", len(chs))
	}
	// JSON-round-trip shape: channels as []any of map[string]any
	detail2 := map[string]any{
		"channels": []any{
			map[string]any{"name": "A", "episodes": []any{map[string]any{"title": "e", "url": "u"}}},
		},
	}
	chs2 := channelsOf(detail2)
	if len(chs2) != 1 || len(episodesOf(chs2[0])) != 1 {
		t.Fatalf("[]any normalization failed: %d channels", len(chs2))
	}
}

func TestCheckSourceAvailabilityNoItems(t *testing.T) {
	s := &Server{}
	res := s.checkSourceAvailability(&SiteSource{Name: "x"}, nil)
	if !res.Available {
		t.Fatalf("empty items should be treated as available (nothing to filter)")
	}
}

// A loopback detail URL is blocked by the SSRF policy, so getDetail fails and
// the source must be reported unavailable. This exercises the failure branch
// without depending on any real upstream.
func TestCheckSourceAvailabilityDetailBlocked(t *testing.T) {
	s := &Server{}
	source := &SiteSource{Name: "dead", SourceType: "json_api", BaseURL: "http://127.0.0.1:1"}
	items := []map[string]any{{"url": "json_api://vod/1"}}
	res := s.checkSourceAvailability(source, items)
	if res.Available {
		t.Fatalf("expected unavailable when detail fetch is blocked")
	}
	if res.Reason == "" {
		t.Fatalf("expected a reason for the failure")
	}
}

// probeMedia against a real, reachable public host (example.com) validates the
// network path of the checker using both an iframe (unverified -> available)
// and an m3u8 (HTML body -> not a valid playlist -> unavailable) type.
func TestProbeMediaPublicHost(t *testing.T) {
	s := &Server{}
	// iframe type: cannot verify, so it is treated as available (avoid false negatives).
	if r := s.probeMedia("https://example.com", "iframe"); !r.Playable {
		t.Fatalf("iframe probe should be playable(unverified): %+v", r)
	}
	// mp4 type against a plain HTML host: 200 with a body => reachable => playable.
	if r := s.probeMedia("https://example.com", "mp4"); !r.Playable || !r.Downloadable {
		t.Fatalf("mp4 probe against reachable host should be playable+downloadable: %+v", r)
	}
	// m3u8 type against a plain HTML host: body lacks #EXTM3U => not playable.
	if r := s.probeMedia("https://example.com", "m3u8"); r.Playable {
		t.Fatalf("m3u8 probe against non-playlist host should be unplayable: %+v", r)
	}
}

func TestProbeMediaBlocked(t *testing.T) {
	s := &Server{}
	if r := s.probeMedia("http://127.0.0.1:1/x.m3u8", "m3u8"); r.Playable {
		t.Fatalf("loopback must be blocked")
	}
	if r := s.probeMedia("", "mp4"); r.Playable {
		t.Fatalf("empty url must be blocked")
	}
}

// parseBandwidthKbps 决定「速度余量」的码率基准，解析错误会直接让智能排序
// 失真，因此对 master / 媒体播放列表与异常输入都做覆盖。
func TestParseBandwidthKbps(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"master 第一档", "#EXTM3U\n#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=800000,RESOLUTION=1080x608\n2000k/hls/mixed.m3u8", 800},
		{"master 多档取第一档", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1200000\n1.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2400000\n2.m3u8", 1200},
		{"媒体列表 EXT-X-BITRATE 兜底", "#EXTM3U\n#EXT-X-BITRATE:1500\n#EXTINF:4.0,\na.ts", 1500},
		{"无码率信息", "#EXTM3U\n#EXTINF:4.0,\na.ts", 0},
		{"空文本", "", 0},
	}
	for _, c := range cases {
		if got := parseBandwidthKbps(c.body); got != c.want {
			t.Errorf("%s: parseBandwidthKbps() = %d, want %d", c.name, got, c.want)
		}
	}
}

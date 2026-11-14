package backend

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// User mirrors the app_user table.
type User struct {
	ID             int64          `json:"id"`
	Username       string         `json:"username"`
	PasswordHash   string         `json:"-"`
	Salt           string         `json:"-"`
	Role           string         `json:"role"`
	Token          sql.NullString `json:"-"`
	TokenExpiresAt sql.NullTime   `json:"-"`
	CreatedAt      string         `json:"createdAt"`
}

// SiteSource mirrors the site_source table. JSON tags match the frontend camelCase contract.
type SiteSource struct {
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	BaseURL               string `json:"baseUrl"`
	SearchURL             string `json:"searchUrl"`
	SearchPostData        string `json:"searchPostData"`
	RuleResultList        string `json:"ruleResultList"`
	RuleResultCover       string `json:"ruleResultCover"`
	ResultCoverPrefix     string `json:"resultCoverPrefix"`
	RuleResultLink        string `json:"ruleResultLink"`
	ResultLinkPrefix      string `json:"resultLinkPrefix"`
	RuleResultTitle       string `json:"ruleResultTitle"`
	RuleDetailCover       string `json:"ruleDetailCover"`
	RuleDetailCoverPrefix string `json:"ruleDetailCoverPrefix"`
	RuleDetailIntro       string `json:"ruleDetailIntro"`
	RuleChannelGroups     string `json:"ruleChannelGroups"`
	RuleEpList            string `json:"ruleEpList"`
	RuleEpTitle           string `json:"ruleEpTitle"`
	RuleEpLink            string `json:"ruleEpLink"`
	RuleEpLinkPrefix      string `json:"ruleEpLinkPrefix"`
	RuleIndex1List        string `json:"ruleIndex1List"`
	Index1Title           string `json:"index1Title"`
	RuleIndex2List        string `json:"ruleIndex2List"`
	Index2Title           string `json:"index2Title"`
	RuleIndex3List        string `json:"ruleIndex3List"`
	Index3Title           string `json:"index3Title"`
	RuleIndex4List        string `json:"ruleIndex4List"`
	Index4Title           string `json:"index4Title"`
	RuleIndexLink         string `json:"ruleIndexLink"`
	IndexLinkPrefix       string `json:"indexLinkPrefix"`
	RuleIndexTitle        string `json:"ruleIndexTitle"`
	RuleIndexCover        string `json:"ruleIndexCover"`
	IndexCoverPrefix      string `json:"indexCoverPrefix"`
	RuleIndexActor        string `json:"ruleIndexActor"`
	WebIndexURL           string `json:"webIndexUrl"`
	UserAgent             string `json:"userAgent"`
	ReqType               string `json:"reqType"`
	ReqCharset            string `json:"reqCharset"`
	HtmlCharset           string `json:"htmlCharset"`
	SerialNumber          int    `json:"serialNumber"`
	IsDisable             int    `json:"isDisable"`
	Enabled               bool   `json:"enabled"`
	SourceType            string `json:"sourceType"`
	ApiDetailURL          string `json:"apiDetailUrl"`
	RemoteConfigURL       string `json:"remoteConfigUrl"`
	BackupBaseURL         string `json:"backupBaseUrl"`
}

// History mirrors the history table.
type History struct {
	ID           int64  `json:"id"`
	UserID       *int64 `json:"userId,omitempty"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	SourceName   string `json:"sourceName"`
	SourceURL    string `json:"sourceUrl"`
	EpisodeTitle string `json:"episodeTitle"`
	PlayURL      string `json:"playUrl"`
	Position     int    `json:"position"`
	Duration     int    `json:"duration"`
	CreatedAt    string `json:"createdAt"`
}

// Favorite mirrors the favorite table.
type Favorite struct {
	ID         int64  `json:"id"`
	UserID     *int64 `json:"userId,omitempty"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	SourceName string `json:"sourceName"`
	DetailURL  string `json:"detailUrl"`
	CreatedAt  string `json:"createdAt"`
}

// LiveChannel mirrors the live_channel table.
type LiveChannel struct {
	ID        int64  `json:"id"`
	GroupName string `json:"groupName"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	SortOrder int    `json:"sortOrder"`
}

// siteSourceColumns is the canonical column order used for SELECT/scan.
var siteSourceColumns = []string{
	"id", "name", "base_url", "search_url", "search_post_data", "rule_result_list",
	"rule_result_cover", "result_cover_prefix", "rule_result_link", "result_link_prefix",
	"rule_result_title", "rule_detail_cover", "rule_detail_cover_prefix", "rule_detail_intro",
	"rule_channel_groups", "rule_ep_list", "rule_ep_title", "rule_ep_link", "rule_ep_link_prefix",
	"rule_index1_list", "index1_title", "rule_index2_list", "index2_title", "rule_index3_list",
	"index3_title", "rule_index4_list", "index4_title", "rule_index_link", "index_link_prefix",
	"rule_index_title", "rule_index_cover", "index_cover_prefix", "rule_index_actor",
	"web_index_url", "user_agent", "req_type", "req_charset", "html_charset",
	"serial_number", "is_disable", "source_type", "api_detail_url", "remote_config_url", "backup_base_url",
}

func (s *Server) openDB(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	// 海报磁盘缓存与 DB 同级（posters/），生命周期一致。
	setPosterCacheDir(path)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	// WAL 模式支持一写多读；放宽连接数让并发读不再被单连接串行化。
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		return err
	}
	s.db = db
	return s.migrate()
}

func (s *Server) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS app_user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			salt TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			token TEXT,
			token_expires_at TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS site_source (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			base_url TEXT UNIQUE NOT NULL,
			search_url TEXT,
			search_post_data TEXT,
			rule_result_list TEXT,
			rule_result_cover TEXT,
			result_cover_prefix TEXT,
			rule_result_link TEXT,
			result_link_prefix TEXT,
			rule_result_title TEXT,
			rule_detail_cover TEXT,
			rule_detail_cover_prefix TEXT,
			rule_detail_intro TEXT,
			rule_channel_groups TEXT,
			rule_ep_list TEXT,
			rule_ep_title TEXT,
			rule_ep_link TEXT,
			rule_ep_link_prefix TEXT,
			rule_index1_list TEXT,
			index1_title TEXT,
			rule_index2_list TEXT,
			index2_title TEXT,
			rule_index3_list TEXT,
			index3_title TEXT,
			rule_index4_list TEXT,
			index4_title TEXT,
			rule_index_link TEXT,
			index_link_prefix TEXT,
			rule_index_title TEXT,
			rule_index_cover TEXT,
			index_cover_prefix TEXT,
			rule_index_actor TEXT,
			web_index_url TEXT,
			user_agent TEXT,
			req_type TEXT DEFAULT 'get',
			req_charset TEXT DEFAULT 'utf-8',
			html_charset TEXT DEFAULT 'utf-8',
			serial_number INTEGER DEFAULT 1,
			is_disable INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			source_type TEXT NOT NULL DEFAULT 'html',
			api_detail_url TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER DEFAULT NULL,
			title TEXT NOT NULL,
			cover TEXT,
			source_name TEXT,
			source_url TEXT,
			episode_title TEXT,
			play_url TEXT,
			play_position INTEGER DEFAULT 0,
			duration INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS favorite (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER DEFAULT NULL,
			title TEXT NOT NULL,
			cover TEXT,
			source_name TEXT,
			detail_url TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, detail_url)
		)`,
		`CREATE TABLE IF NOT EXISTS live_channel (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_name TEXT NOT NULL,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			sort_order INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_prefs (
			user_id INTEGER NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (user_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS douban_list (
			cache_key TEXT PRIMARY KEY,
			items TEXT NOT NULL,
			fetched_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS avail_cache (
			source_name TEXT NOT NULL,
			base_url TEXT NOT NULL,
			available INTEGER NOT NULL DEFAULT 0,
			playable INTEGER NOT NULL DEFAULT 0,
			downloadable INTEGER NOT NULL DEFAULT 0,
			reason TEXT,
			expires_at TEXT,
			PRIMARY KEY (source_name, base_url)
		)`,
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	addCols := []string{
		"ALTER TABLE site_source ADD COLUMN source_type TEXT NOT NULL DEFAULT 'html'",
		"ALTER TABLE site_source ADD COLUMN api_detail_url TEXT",
		"ALTER TABLE site_source ADD COLUMN remote_config_url TEXT",
		"ALTER TABLE site_source ADD COLUMN backup_base_url TEXT",
		"ALTER TABLE history ADD COLUMN user_id INTEGER DEFAULT NULL",
		"ALTER TABLE favorite ADD COLUMN user_id INTEGER DEFAULT NULL",
		"ALTER TABLE app_user ADD COLUMN token_expires_at TIMESTAMP",
		// 站源网络质量指标（智能排序依据）：TTFB / 实测吞吐 / 声明码率
		"ALTER TABLE avail_cache ADD COLUMN ttfb_ms INTEGER DEFAULT 0",
		"ALTER TABLE avail_cache ADD COLUMN speed_kbps INTEGER DEFAULT 0",
		"ALTER TABLE avail_cache ADD COLUMN bitrate_kbps INTEGER DEFAULT 0",
	}
	for _, st := range addCols {
		_, _ = s.db.Exec(st)
	}
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_user_token ON app_user(token)")
	// 历史记录按用户查询是频繁操作，添加索引避免全表扫描
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_history_user_id ON history(user_id)")
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_history_created_at ON history(created_at DESC)")
	// 收藏按用户查询；UNIQUE(user_id, detail_url) 已建索引，单独 user_id 索引加速列表查询
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_favorite_user_id ON favorite(user_id)")

	_, _ = s.db.Exec("DELETE FROM site_source WHERE base_url = ?", "https://cj.lzcaiji.com")
	// 方案 C（2026-08-01）：移除「量子M3U8资源」「豆瓣M3U8资源」两个冗余 from 过滤行。
	// 它们与「量子资源」「豆瓣资源」同 host，仅多 from 过滤；子源(线路)选择已由前端运行时完成
	// （详情返回的 vod_play_from 即按 liangzi/lzm3u8/dbm3u8 等线路分组），合并还可减少搜索结果重复。
	// 清理旧库中已 seed 的这两行（seed 仅 insert-if-not-exists，不会自动删除）。
	_, _ = s.db.Exec("DELETE FROM site_source WHERE base_url = ?", "https://cj.lziapi.com/api.php/provide/vod/from/lzm3u8/at/josn/")
	_, _ = s.db.Exec("DELETE FROM site_source WHERE base_url = ?", "https://caiji.dbzy5.com/api.php/provide/vod/from/dbm3u8/at/josn/")
	_, _ = s.db.Exec("UPDATE site_source SET backup_base_url = 'http://cj.ffzyapi.com' WHERE base_url = 'http://api.ffzyapi.com' AND (backup_base_url IS NULL OR backup_base_url = '')")
	_, _ = s.db.Exec("UPDATE site_source SET backup_base_url = 'https://dbzy.tv' WHERE base_url = 'https://caiji.dbzy5.com' AND (backup_base_url IS NULL OR backup_base_url = '')")

	s.seed()
	return nil
}

func (s *Server) seed() {
	s.seedPresetSources()
	s.seedAdminUser()
	s.seedSettings()
}

func (s *Server) seedPresetSources() {
	s.seedSource("https://cj.lziapi.com", "量子资源",
		"/api.php/provide/vod/at/josn/?ac=list&wd={search}",
		"/api.php/provide/vod/at/josn/?ac=detail&ids={id}",
		"https://cj.lzcaiji.com")
	// 方案 C：原「量子M3U8资源」(from=lzm3u8 过滤) 已移除。量子资源的详情会返回
	// vod_play_from（含 liangzi、lzm3u8 等线路），子源由前端运行时在「线路」中选择，无需独立行。
	s.seedSource("https://jyzyapi.com", "金鹰资源网",
		"/provide/vod/at/json/?ac=list&wd={search}",
		"/provide/vod/at/json/?ac=detail&ids={id}", "")
	s.seedSource("http://api.ffzyapi.com", "非凡资源",
		"/api.php/provide/vod/at/json/?ac=list&wd={search}",
		"/api.php/provide/vod/at/json/?ac=detail&ids={id}",
		"http://cj.ffzyapi.com")
	s.seedSource("https://caiji.dbzy5.com", "豆瓣资源",
		"/api.php/provide/vod/at/josn/?ac=list&wd={search}",
		"/api.php/provide/vod/at/josn/?ac=detail&ids={id}",
		"https://dbzy.tv")
	// 方案 C：原「豆瓣M3U8资源」(from=dbm3u8 过滤) 已移除，同上（子源由前端运行时选择）。
	// 资源站（MacCMS json_api）
	s.seedSource("https://bfzyapi.com", "暴风资源站",
		"/api.php/provide/vod/?ac=list&wd={search}", "/api.php/provide/vod/?ac=detail&ids={id}", "")
	s.seedSource("https://jszyapi.com", "极速资源站",
		"/api.php/provide/vod/?ac=list&wd={search}", "/api.php/provide/vod/?ac=detail&ids={id}", "")
	s.seedSource("https://subocaiji.com", "速博资源网",
		"/api.php/provide/vod/?ac=list&wd={search}", "/api.php/provide/vod/?ac=detail&ids={id}", "")
	s.seedSource("https://api.zuidapi.com", "最大资源网",
		"/api.php/provide/vod/?ac=list&wd={search}", "/api.php/provide/vod/?ac=detail&ids={id}", "")
}

func (s *Server) seedSource(baseURL, name, searchURL, detailURL, backup string) {
	var cnt int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM site_source WHERE base_url = ?", baseURL).Scan(&cnt)
	if cnt > 0 {
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO site_source
		 (name, base_url, search_url, api_detail_url, source_type, backup_base_url,
		  serial_number, is_disable, user_agent, req_charset, html_charset)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		name, baseURL, searchURL, detailURL, "json_api", backup,
		0, 0, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"utf-8", "utf-8"); err != nil {
		logWarn("seed source %s: %v", name, err)
	}
}

func (s *Server) seedAdminUser() {
	var cnt int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM app_user WHERE username = 'admin'").Scan(&cnt)
	if cnt > 0 {
		return
	}
	// bcrypt 自带 salt，旧版 generateSalt() 不再需要
	hash := hashPassword("admin")
	token := newUUID()
	if _, err := s.db.Exec(
		`INSERT INTO app_user (username, password_hash, salt, role, token, token_expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"admin", hash, "", "admin", token, time.Now().Add(TokenExpiry).Format(time.RFC3339), time.Now().Format(time.RFC3339)); err != nil {
		logWarn("seed admin: %v", err)
		return
	}
	logPrint("Admin user seeded. Default password: admin - please change it after first login.")
}

// seedSettings 写入系统级开关的默认值。仅当 key 不存在时插入，不覆盖管理员
// 在运行时修改的值（upsert 用 INSERT OR IGNORE 语义）。
func (s *Server) seedSettings() {
	// registration_open：是否开放新用户注册。默认开启（"true"）。
	var cnt int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM settings WHERE key = ?", "registration_open").Scan(&cnt)
	if cnt == 0 {
		if _, err := s.db.Exec(
			`INSERT INTO settings (key, value) VALUES (?, ?)`,
			"registration_open", "true"); err != nil {
			logWarn("seed settings: %v", err)
		}
	}
	// default_play_mode：默认播放方式。online=在线播放（默认），progressive=边下边播。
	var cnt2 int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM settings WHERE key = ?", "default_play_mode").Scan(&cnt2)
	if cnt2 == 0 {
		if _, err := s.db.Exec(
			`INSERT INTO settings (key, value) VALUES (?, ?)`,
			"default_play_mode", "online"); err != nil {
			logWarn("seed settings: %v", err)
		}
	}
}

func ns(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func scanSiteSource(rows *sql.Rows) (*SiteSource, error) {
	var (
		id, serial, disable                                          sql.NullInt64
		name, baseURL, searchURL, searchPost, rList, rCover, rCovPre sql.NullString
		rLink, rLinkPre, rTitle, dCover, dCovPre, dIntro             sql.NullString
		chGroups, epList, epTitle, epLink, epLinkPre                 sql.NullString
		i1List, i1Title, i2List, i2Title, i3List, i3Title            sql.NullString
		i4List, i4Title, iLink, iLinkPre, iTitle, iCover             sql.NullString
		iCovPre, iActor, webIndex, ua, reqType, reqCharset           sql.NullString
		htmlCharset, sourceType, apiDetail, remoteCfg, backup        sql.NullString
	)
	err := rows.Scan(
		&id, &name, &baseURL, &searchURL, &searchPost, &rList,
		&rCover, &rCovPre, &rLink, &rLinkPre,
		&rTitle, &dCover, &dCovPre, &dIntro,
		&chGroups, &epList, &epTitle, &epLink, &epLinkPre,
		&i1List, &i1Title, &i2List, &i2Title, &i3List,
		&i3Title, &i4List, &i4Title, &iLink, &iLinkPre,
		&iTitle, &iCover, &iCovPre, &iActor,
		&webIndex, &ua, &reqType, &reqCharset, &htmlCharset,
		&serial, &disable, &sourceType, &apiDetail, &remoteCfg, &backup,
	)
	if err != nil {
		return nil, err
	}
	return &SiteSource{
		ID: id.Int64, Name: ns(name), BaseURL: ns(baseURL), SearchURL: ns(searchURL),
		SearchPostData: ns(searchPost), RuleResultList: ns(rList), RuleResultCover: ns(rCover),
		ResultCoverPrefix: ns(rCovPre), RuleResultLink: ns(rLink), ResultLinkPrefix: ns(rLinkPre),
		RuleResultTitle: ns(rTitle), RuleDetailCover: ns(dCover), RuleDetailCoverPrefix: ns(dCovPre),
		RuleDetailIntro: ns(dIntro), RuleChannelGroups: ns(chGroups), RuleEpList: ns(epList),
		RuleEpTitle: ns(epTitle), RuleEpLink: ns(epLink), RuleEpLinkPrefix: ns(epLinkPre),
		RuleIndex1List: ns(i1List), Index1Title: ns(i1Title), RuleIndex2List: ns(i2List),
		Index2Title: ns(i2Title), RuleIndex3List: ns(i3List), Index3Title: ns(i3Title),
		RuleIndex4List: ns(i4List), Index4Title: ns(i4Title), RuleIndexLink: ns(iLink),
		IndexLinkPrefix: ns(iLinkPre), RuleIndexTitle: ns(iTitle), RuleIndexCover: ns(iCover),
		IndexCoverPrefix: ns(iCovPre), RuleIndexActor: ns(iActor), WebIndexURL: ns(webIndex),
		UserAgent: ns(ua), ReqType: ns(reqType), ReqCharset: ns(reqCharset),
		HtmlCharset: ns(htmlCharset), SerialNumber: int(serial.Int64), IsDisable: int(disable.Int64),
		Enabled:    int(disable.Int64) == 0,
		SourceType: ns(sourceType), ApiDetailURL: ns(apiDetail), RemoteConfigURL: ns(remoteCfg),
		BackupBaseURL: ns(backup),
	}, nil
}

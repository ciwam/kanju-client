package backend

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *Server) getAllSiteSources() ([]*SiteSource, error) {
	rows, err := s.db.Query("SELECT " + strings.Join(siteSourceColumns, ",") +
		" FROM site_source ORDER BY serial_number ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SiteSource{}
	for rows.Next() {
		ss, err := scanSiteSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Server) getActiveSiteSources() ([]*SiteSource, error) {
	rows, err := s.db.Query("SELECT " + strings.Join(siteSourceColumns, ",") +
		" FROM site_source WHERE is_disable = 0 ORDER BY serial_number ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SiteSource{}
	for rows.Next() {
		ss, err := scanSiteSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Server) getSiteSourceByBaseURL(baseURL string) (*SiteSource, error) {
	rows, err := s.db.Query("SELECT "+strings.Join(siteSourceColumns, ",")+
		" FROM site_source WHERE base_url = ?", baseURL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanSiteSource(rows)
	}
	return nil, nil
}

func (s *Server) saveSiteSource(m map[string]any) error {
	ss := siteSourceFromMap(m)
	if ss.BaseURL == "" {
		return fmt.Errorf("baseUrl cannot be empty")
	}
	existing, _ := s.getSiteSourceByBaseURL(ss.BaseURL)
	var err error
	if existing != nil {
		ss.ID = existing.ID
		_, err = s.db.Exec(
			`UPDATE site_source SET
				name=?, search_url=?, search_post_data=?, rule_result_list=?, rule_result_cover=?,
				result_cover_prefix=?, rule_result_link=?, result_link_prefix=?, rule_result_title=?,
				rule_detail_cover=?, rule_detail_cover_prefix=?, rule_detail_intro=?, rule_channel_groups=?,
				rule_ep_list=?, rule_ep_title=?, rule_ep_link=?, rule_ep_link_prefix=?,
				rule_index1_list=?, index1_title=?, rule_index2_list=?, index2_title=?,
				rule_index3_list=?, index3_title=?, rule_index4_list=?, index4_title=?,
				rule_index_link=?, index_link_prefix=?, rule_index_title=?, rule_index_cover=?,
				index_cover_prefix=?, rule_index_actor=?, web_index_url=?, user_agent=?,
				req_type=?, req_charset=?, html_charset=?, serial_number=?, is_disable=?,
				source_type=?, api_detail_url=?, remote_config_url=?, backup_base_url=?
			 WHERE base_url=?`,
			ss.Name, ss.SearchURL, ss.SearchPostData, ss.RuleResultList, ss.RuleResultCover,
			ss.ResultCoverPrefix, ss.RuleResultLink, ss.ResultLinkPrefix, ss.RuleResultTitle,
			ss.RuleDetailCover, ss.RuleDetailCoverPrefix, ss.RuleDetailIntro, ss.RuleChannelGroups,
			ss.RuleEpList, ss.RuleEpTitle, ss.RuleEpLink, ss.RuleEpLinkPrefix,
			ss.RuleIndex1List, ss.Index1Title, ss.RuleIndex2List, ss.Index2Title,
			ss.RuleIndex3List, ss.Index3Title, ss.RuleIndex4List, ss.Index4Title,
			ss.RuleIndexLink, ss.IndexLinkPrefix, ss.RuleIndexTitle, ss.RuleIndexCover,
			ss.IndexCoverPrefix, ss.RuleIndexActor, ss.WebIndexURL, ss.UserAgent,
			ss.ReqType, ss.ReqCharset, ss.HtmlCharset, ss.SerialNumber, ss.IsDisable,
			ss.SourceType, ss.ApiDetailURL, ss.RemoteConfigURL, ss.BackupBaseURL, ss.BaseURL)
	} else {
		_, err = s.db.Exec(
			`INSERT INTO site_source
			 (name, base_url, search_url, search_post_data, rule_result_list, rule_result_cover,
			  result_cover_prefix, rule_result_link, result_link_prefix, rule_result_title,
			  rule_detail_cover, rule_detail_cover_prefix, rule_detail_intro, rule_channel_groups,
			  rule_ep_list, rule_ep_title, rule_ep_link, rule_ep_link_prefix,
			  rule_index1_list, index1_title, rule_index2_list, index2_title,
			  rule_index3_list, index3_title, rule_index4_list, index4_title,
			  rule_index_link, index_link_prefix, rule_index_title, rule_index_cover,
			  index_cover_prefix, rule_index_actor, web_index_url, user_agent,
			  req_type, req_charset, html_charset, serial_number, is_disable,
			  source_type, api_detail_url, remote_config_url, backup_base_url)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ss.Name, ss.BaseURL, ss.SearchURL, ss.SearchPostData, ss.RuleResultList, ss.RuleResultCover,
			ss.ResultCoverPrefix, ss.RuleResultLink, ss.ResultLinkPrefix, ss.RuleResultTitle,
			ss.RuleDetailCover, ss.RuleDetailCoverPrefix, ss.RuleDetailIntro, ss.RuleChannelGroups,
			ss.RuleEpList, ss.RuleEpTitle, ss.RuleEpLink, ss.RuleEpLinkPrefix,
			ss.RuleIndex1List, ss.Index1Title, ss.RuleIndex2List, ss.Index2Title,
			ss.RuleIndex3List, ss.Index3Title, ss.RuleIndex4List, ss.Index4Title,
			ss.RuleIndexLink, ss.IndexLinkPrefix, ss.RuleIndexTitle, ss.RuleIndexCover,
			ss.IndexCoverPrefix, ss.RuleIndexActor, ss.WebIndexURL, ss.UserAgent,
			ss.ReqType, ss.ReqCharset, ss.HtmlCharset, ss.SerialNumber, ss.IsDisable,
			ss.SourceType, ss.ApiDetailURL, ss.RemoteConfigURL, ss.BackupBaseURL)
	}
	return err
}

func (s *Server) deleteSiteSource(baseURL string) error {
	_, err := s.db.Exec("DELETE FROM site_source WHERE base_url = ?", baseURL)
	return err
}

func (s *Server) toggleSiteSource(baseURL string) error {
	ss, err := s.getSiteSourceByBaseURL(baseURL)
	if err != nil || ss == nil {
		return fmt.Errorf("site source not found")
	}
	newVal := 0
	if ss.IsDisable == 0 {
		newVal = 1
	}
	_, err = s.db.Exec("UPDATE site_source SET is_disable = ? WHERE base_url = ?", newVal, baseURL)
	return err
}

// ---- history ----

func (s *Server) saveHistory(userID *int64, title, cover, sourceName, sourceURL, detailURL, episodeTitle, playURL string, pos, dur int) error {
	// 规整：去除首尾空白，避免空格差异造成"看似不同"的重复键。
	title = strings.TrimSpace(title)
	sourceURL = strings.TrimSpace(sourceURL)
	episodeTitle = strings.TrimSpace(episodeTitle)
	playURL = strings.TrimSpace(playURL)

	// 异常记录防护：既没有标题也没有可标识来源(source_url)的播放记录，既无法在
	// 列表中展示也无法续播，属于异常数据。直接丢弃，避免产生无法去重的"空键"重复
	// 记录（典型场景：详情页经深链续播时 data 尚未就绪，写入了空 title/空 source_url）。
	if title == "" && sourceURL == "" {
		// 没有标题也没有可标识来源，属异常数据，直接丢弃，避免产生无法去重的"空键"记录
		return nil
	}

	// 去重键：优先用 source_url 标识影片；当 source_url 为空时回退到 play_url，
	// 保证同源不同入口(详情页 / 独立播放页)的记录能正确合并为同一条。
	identity := sourceURL
	if identity == "" {
		identity = playURL
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"DELETE FROM history WHERE (user_id IS ? OR (user_id IS NULL AND ? IS NULL)) AND (source_url = ? OR (source_url = '' AND play_url = ?)) AND episode_title = ?",
		userID, userID, identity, identity, episodeTitle); err != nil {
		return err
	}
	if pos < 0 {
		pos = 0
	}
	if dur < 0 {
		dur = 0
	}
	if _, err := tx.Exec(
		`INSERT INTO history (user_id, title, cover, source_name, source_url, detail_url, episode_title, play_url, play_position, duration, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, title, cover, sourceName, sourceURL, detailURL, episodeTitle, playURL, pos, dur, time.Now().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) getHistory(userID *int64) ([]*History, error) {
	var rows *sql.Rows
	var err error
	if userID != nil {
		rows, err = s.db.Query(
			"SELECT id, user_id, title, cover, source_name, source_url, detail_url, episode_title, play_url, play_position, duration, created_at FROM history WHERE user_id = ? ORDER BY created_at DESC LIMIT 100", *userID)
	} else {
		rows, err = s.db.Query(
			"SELECT id, user_id, title, cover, source_name, source_url, detail_url, episode_title, play_url, play_position, duration, created_at FROM history WHERE user_id IS NULL ORDER BY created_at DESC LIMIT 100")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistory(rows)
}

func (s *Server) getAllHistory() ([]*History, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, title, cover, source_name, source_url, detail_url, episode_title, play_url, play_position, duration, created_at FROM history ORDER BY created_at DESC LIMIT 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistory(rows)
}

func scanHistory(rows *sql.Rows) ([]*History, error) {
	out := []*History{}
	for rows.Next() {
		var h History
		var uid sql.NullInt64
		if err := rows.Scan(&h.ID, &uid, &h.Title, &h.Cover, &h.SourceName, &h.SourceURL,
			&h.DetailURL, &h.EpisodeTitle, &h.PlayURL, &h.Position, &h.Duration, &h.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.Int64
			h.UserID = &v
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// ---- favorites ----

func (s *Server) saveFavorite(userID *int64, title, cover, sourceName, detailURL string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO favorite (user_id, title, cover, source_name, detail_url, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, title, cover, sourceName, detailURL, time.Now().Format(time.RFC3339))
	return err
}

func (s *Server) getFavorites(userID *int64) ([]*Favorite, error) {
	var rows *sql.Rows
	var err error
	if userID != nil {
		rows, err = s.db.Query(
			"SELECT id, user_id, title, cover, source_name, detail_url, created_at FROM favorite WHERE user_id = ? ORDER BY created_at DESC", *userID)
	} else {
		rows, err = s.db.Query(
			"SELECT id, user_id, title, cover, source_name, detail_url, created_at FROM favorite WHERE user_id IS NULL ORDER BY created_at DESC")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFavorites(rows)
}

func (s *Server) getAllFavorites() ([]*Favorite, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, title, cover, source_name, detail_url, created_at FROM favorite ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFavorites(rows)
}

func scanFavorites(rows *sql.Rows) ([]*Favorite, error) {
	out := []*Favorite{}
	for rows.Next() {
		var f Favorite
		var uid sql.NullInt64
		if err := rows.Scan(&f.ID, &uid, &f.Title, &f.Cover, &f.SourceName, &f.DetailURL, &f.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.Int64
			f.UserID = &v
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

func (s *Server) removeFavorite(userID *int64, detailURL string) error {
	if userID != nil {
		_, err := s.db.Exec("DELETE FROM favorite WHERE detail_url = ? AND user_id = ?", detailURL, *userID)
		return err
	}
	_, err := s.db.Exec("DELETE FROM favorite WHERE detail_url = ?", detailURL)
	return err
}

// ---- live ----

func (s *Server) getLiveChannels(group string) ([]*LiveChannel, error) {
	var rows *sql.Rows
	var err error
	if group != "" {
		rows, err = s.db.Query("SELECT id, group_name, name, url, sort_order FROM live_channel WHERE group_name = ? ORDER BY sort_order ASC", group)
	} else {
		rows, err = s.db.Query("SELECT id, group_name, name, url, sort_order FROM live_channel ORDER BY sort_order ASC")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LiveChannel{}
	for rows.Next() {
		var lc LiveChannel
		if err := rows.Scan(&lc.ID, &lc.GroupName, &lc.Name, &lc.URL, &lc.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, &lc)
	}
	return out, rows.Err()
}

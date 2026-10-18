package store

import (
	"database/sql"
	"encoding/json"
)

// JSONColumnCorruption 一条可定位的元数据损坏：哪部影片的哪一列不是合法 JSON。
type JSONColumnCorruption struct {
	MovieID int64  `json:"movie_id"`
	Column  string `json:"column"`
}

// JSONCorruptionReport 全库 JSON 列体检结果。Items 只保留前 maxItems 条，
// Total 是全量计数，用于判断是否需要人工修复。
type JSONCorruptionReport struct {
	Total int                    `json:"total"`
	Items []JSONColumnCorruption `json:"items"`
}

var jsonDiagnosticColumns = []string{"genres", "tags", "studios", "taglines", "additional_parts"}

// CorruptedJSON 扫描元数据 JSON 列，给出可定位的损坏清单（DB-10）。
//
// 读路径对坏值静默降级为空切片（parseStrings），适合展示但不适合排障；
// 这里返回影片 id + 列名，管理端可据此筛选后人工修复或重扫。
func (s *Store) CorruptedJSON(maxItems int) (JSONCorruptionReport, error) {
	if maxItems <= 0 {
		maxItems = 20
	}
	rows, err := s.db.Query("SELECT id,genres,tags,studios,taglines,additional_parts FROM movies ORDER BY id")
	if err != nil {
		return JSONCorruptionReport{}, err
	}
	defer rows.Close()
	report := JSONCorruptionReport{Items: []JSONColumnCorruption{}}
	for rows.Next() {
		var id int64
		values := make([]sql.NullString, len(jsonDiagnosticColumns))
		dest := make([]any, 0, len(values)+1)
		dest = append(dest, &id)
		for i := range values {
			dest = append(dest, &values[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return report, err
		}
		for i, value := range values {
			if !value.Valid || value.String == "" {
				continue
			}
			if json.Valid([]byte(value.String)) {
				continue
			}
			report.Total++
			if len(report.Items) < maxItems {
				report.Items = append(report.Items, JSONColumnCorruption{MovieID: id, Column: jsonDiagnosticColumns[i]})
			}
		}
	}
	return report, rows.Err()
}

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// entityScope intersects an explicit library with grants as well as applying
// collection/status filters. A JSON-bound set uses one parameter at any size.
func entityScope(libraryID int64, visible []int64, collection string) (string, []any) {
	where := "movies.status IN ('success','manual')"
	args := []any{}
	if libraryID > 0 {
		where += " AND movies.library_id=?"
		args = append(args, libraryID)
	}
	if visible != nil {
		if len(visible) == 0 {
			where += " AND 1=0"
		} else {
			raw, _ := json.Marshal(visible)
			where += " AND movies.library_id IN (SELECT value FROM json_each(?))"
			args = append(args, string(raw))
		}
	}
	if collection == "*" {
		where += " AND COALESCE(movies.collection,'')<>''"
	} else if collection != "" {
		where += " AND movies.collection=?"
		args = append(args, collection)
	}
	return where, args
}

// EntityNamesVisible filters the movie set before aggregating entity names.
// nil visible is unrestricted; an empty, non-nil set grants no libraries.
func (s *Store) EntityNamesVisible(kind string, libraryID int64, visible []int64, collection string) ([]string, error) {
	column, ok := entityColumns[kind]
	if !ok && kind != "Person" {
		return []string{}, nil
	}
	where, args := entityScope(libraryID, visible, collection)
	query := "SELECT COALESCE(movies." + column + ",'') FROM movies WHERE " + where
	if kind == "Person" {
		query = "SELECT DISTINCT ma.actor_name FROM movies JOIN movie_actors ma ON ma.movie_id=movies.id WHERE " + where
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		names := []string{raw}
		if kind != "Person" {
			names = parseStrings(raw)
		}
		for _, name := range names {
			if name = strings.TrimSpace(name); name != "" {
				seen[name] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// EntityPosterVisible chooses art only from the same authorized movie scope.
func (s *Store) EntityPosterVisible(kind, name string, libraryID int64, visible []int64, collection string) (string, error) {
	column, ok := entityColumns[kind]
	if !ok && kind != "Person" {
		return "", nil
	}
	where, args := entityScope(libraryID, visible, collection)
	where += " AND COALESCE(movies.poster_path,'')<>''"
	if kind == "Person" {
		where += " AND EXISTS (SELECT 1 FROM movie_actors ma WHERE ma.movie_id=movies.id AND ma.actor_name=?)"
		args = append(args, name)
	} else {
		where += " AND movies." + column + " LIKE ? ESCAPE '\\'"
		args = append(args, "%\""+escapeLike(strings.ReplaceAll(name, `"`, ""))+"\"%")
	}
	var path string
	err := s.db.QueryRow("SELECT movies.poster_path FROM movies WHERE "+where+" ORDER BY movies.id DESC LIMIT 1", args...).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return path, err
}

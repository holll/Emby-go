package nfo

import (
	"strings"
	"testing"
)

func TestManualExplicitClearPreservesOtherXML(t *testing.T) {
	original := strings.Replace(existingNFO, "</movie>", `<vendor:plot xmlns:vendor="urn:vendor">extension</vendor:plot><vendor:title xmlns:vendor="urn:vendor">extension title</vendor:title><vendor:genre xmlns:vendor="urn:vendor">extension genre</vendor:genre><vendor:year xmlns:vendor="urn:vendor">2099</vendor:year><custom><title>nested</title><genre>nested genre</genre></custom></movie>`, 1)
	for _, mode := range []string{"patch", "legacy manual", "scraper"} {
		t.Run(mode, func(t *testing.T) {
			path := writeTempNFO(t, original)
			fields := ScrapeFields{Overwrite: true, ManualEdit: mode != "scraper"}
			if mode != "legacy manual" {
				fields.ClearFields = []string{"title", "originaltitle", "plot", "year", "genre", "tag", "studio"}
			}
			if err := UpdateScraped(path, fields); err != nil {
				t.Fatal(err)
			}
			got := readTempNFO(t, path)
			if mode != "patch" {
				if got != original {
					t.Fatalf("legacy/scraper empty fields changed XML: %s", got)
				}
				return
			}
			meta, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Title != "" || meta.OriginalTitle != "" || meta.Plot != "" || meta.Year != 0 || len(meta.Genres) != 0 || len(meta.Tags) != 0 || len(meta.Studios) != 0 {
				t.Fatalf("not cleared: %+v", meta)
			}
			for _, keep := range []string{`<?xml version="1.0" encoding="UTF-8"?>`, `<!-- 手工注释，必须保留 -->`, `<uniqueid type="tmdb">12345</uniqueid>`, `<name>旧演员</name>`, `<size>999</size>`, `<vendor:plot xmlns:vendor="urn:vendor">extension</vendor:plot>`, `<custom><title>nested</title><genre>nested genre</genre></custom>`} {
				if !strings.Contains(got, keep) {
					t.Fatalf("lost %s: %s", keep, got)
				}
			}
		})
	}
}

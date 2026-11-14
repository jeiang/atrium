package media

import "github.com/jeiang/atrium/internal/connections"

func ImportV2(values map[string]string, tmdb connections.ImportCredential) []connections.ImportService {
	tmdbURL := values[KeyTMDBURL]
	if tmdbURL == "" {
		tmdbURL = DefaultTMDBAPI
	}
	images := values[KeyTMDBImagesURL]
	if images == "" {
		images = DefaultTMDBImages
	}
	eztvURL := values[KeyEZTVURL]
	if eztvURL == "" {
		eztvURL = DefaultEZTVURL
	}
	return []connections.ImportService{
		{
			ID:           string(KindTMDB),
			Kind:         string(KindTMDB),
			Label:        "TMDB",
			Config:       map[string]string{"url": tmdbURL, "images_url": images},
			Transport:    map[string]string{"ca_file": values["tmdb_ca_file"], "proxy": values["tmdb_proxy"]},
			Capabilities: []string{"api", "images"},
			Credentials:  []connections.ImportCredential{tmdb},
		},
		{
			ID:           string(KindEZTV),
			Kind:         string(KindEZTV),
			Label:        "EZTV",
			Config:       map[string]string{"url": eztvURL},
			Capabilities: []string{"indexer"},
		},
	}
}

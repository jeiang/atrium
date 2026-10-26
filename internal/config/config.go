package config

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

const (
	ICloud                  = "icloud"
	Gmail                   = "gmail"
	IMAPPort                = 993
	BodyCapBytes            = 65536
	EmbedDim                = 768
	FetchBatch              = 40
	EmbedBatch              = 16
	KNNK                    = 7
	KNNMinShare             = 0.9
	RuleMinN                = 3
	RuleMinPurity           = 0.9
	FolderMinMessages       = 10
	LLMNeighbors            = 10
	LLMTopSenders           = 3
	LLMBodyChars            = 1500
	EmbedBodyChars          = 1500
	FTSBodyChars            = 20000
	IdleSeconds             = 29 * 60
	IdleSettleSeconds       = 2
	BackoffStart            = 5
	BackoffMax              = 300
	GmailAllMail            = "[Gmail]/All Mail"
	Inbox                   = "INBOX"
	PauseFile               = "pause"
	WriteCap                = 100
	WriteAttempts           = 3
	DeletionIntervalSeconds = 86400
	Timezone                = "America/Port_of_Spain"
	FilesDir                = "files"
	MediaLinkKeyFile        = "media-link.key"
	MediaCacheSubdir        = "media"
	NotionLockName          = "notion"
)

var GmailCategories = [...]string{"social", "promotions", "updates", "forums"}

type Config struct {
	DBPath   string
	CacheDir string
}

func (c Config) PausePath() string { return c.sibling(PauseFile) }

func (c Config) FilesDir() string { return c.sibling(FilesDir) }

func (c Config) MediaLinkKeyPath() string { return c.sibling(MediaLinkKeyFile) }

func (c Config) MediaCacheDir() string { return filepath.Join(c.CacheDir, MediaCacheSubdir) }

func (c Config) LockPath() string { return LockPath(c.DBPath) }

func (c Config) NotionLockPath() string { return LockPath(c.sibling(NotionLockName)) }

func (c Config) sibling(name string) string {
	return filepath.Join(filepath.Dir(c.DBPath), name)
}

func LockPath(path string) string { return path + ".lock" }

func (c Config) Credential(account string) (user, password string, ok bool) {
	a, found := Account(account)
	if !found || !a.Enabled || a.Login == "" || a.Password == "" {
		return "", "", false
	}
	return a.Login, a.Password, true
}

func (c Config) String() string {
	return fmt.Sprintf("Config(db_path=%s, cache_dir=%s)", c.DBPath, c.CacheDir)
}

func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("DB_PATH", c.DBPath),
		slog.String("CACHE_DIR", c.CacheDir),
	)
}

func Load(env Env) (Config, error) {
	state, _ := env("STATE_DIRECTORY")
	state, _, _ = strings.Cut(state, ":")
	db := get(env, "ATRIUM_DB")
	if db == "" && state != "" {
		db = filepath.Join(state, "atrium.db")
	}
	if db == "" {
		return Config{}, errors.New("ATRIUM_DB or STATE_DIRECTORY is required")
	}
	cache := get(env, "ATRIUM_CACHE_DIR")
	if cache == "" && state != "" {
		cache = filepath.Join(state, "cache")
	}
	if cache == "" {
		return Config{}, errors.New("ATRIUM_CACHE_DIR or STATE_DIRECTORY is required")
	}
	accounts, err := loadAccounts(env)
	if err != nil {
		return Config{}, err
	}
	liveAccounts.Store(&accounts)
	return Config{
		DBPath:   filepath.Clean(db),
		CacheDir: filepath.Clean(cache),
	}, nil
}

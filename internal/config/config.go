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

type AccountSpec struct {
	Name          string
	Host          string
	UserEnv       string
	PasswordEnv   string
	UncertainDest string
}

var Accounts = [...]AccountSpec{
	{ICloud, "imap.mail.me.com", "ICLOUD_MAIL_USERNAME", "ICLOUD_APP_PASSWORD", "Misc"},
	{Gmail, "imap.gmail.com", "GMAIL_ADDRESS", "GMAIL_APP_PASSWORD", ""},
}

func Account(name string) (AccountSpec, bool) {
	for _, spec := range Accounts {
		if spec.Name == name {
			return spec, true
		}
	}
	return AccountSpec{}, false
}

type Config struct {
	DBPath      string
	CacheDir    string
	Mode        string
	ChatURL     string
	EmbedURL    string
	ContactsDir string
	StaticDir   string
	credentials map[string]string
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
	spec, found := Account(account)
	if !found {
		return "", "", false
	}
	user = c.credentials[spec.UserEnv]
	password = strings.Join(pySplit(c.credentials[spec.PasswordEnv]), "")
	if user == "" || password == "" {
		return "", "", false
	}
	return user, password, true
}

func (c Config) String() string {
	return fmt.Sprintf("Config(db_path=%s, cache_dir=%s, mode=%s, chat_url=%s, embed_url=%s, contacts_dir=%s, static_dir=%s)",
		c.DBPath, c.CacheDir, c.Mode, c.ChatURL, c.EmbedURL, c.ContactsDir, c.StaticDir)
}

func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("DB_PATH", c.DBPath),
		slog.String("CACHE_DIR", c.CacheDir),
		slog.String("MODE", c.Mode),
		slog.String("CHAT_URL", c.ChatURL),
		slog.String("EMBED_URL", c.EmbedURL),
		slog.String("CONTACTS_DIR", c.ContactsDir),
		slog.String("STATIC_DIR", c.StaticDir),
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
	mode, err := Mode(env)
	if err != nil {
		return Config{}, err
	}
	creds := map[string]string{}
	for _, spec := range Accounts {
		for _, name := range [...]string{spec.UserEnv, spec.PasswordEnv} {
			if v, ok := env(name); ok {
				creds[name] = v
			}
		}
	}
	return Config{
		DBPath:      filepath.Clean(db),
		CacheDir:    filepath.Clean(cache),
		Mode:        mode,
		ChatURL:     strings.TrimRight(get(env, "ATRIUM_CHAT_URL"), "/"),
		EmbedURL:    strings.TrimRight(get(env, "ATRIUM_EMBED_URL"), "/"),
		ContactsDir: optPath(get(env, "ATRIUM_CONTACTS_DIR")),
		StaticDir:   optPath(get(env, "ATRIUM_STATIC_DIR")),
		credentials: creds,
	}, nil
}

func optPath(v string) string {
	if v == "" {
		return ""
	}
	return filepath.Clean(v)
}

package backup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	JobKey          = module.Core + "/" + passrun.Backup
	KeyDirectory    = "backup_directory"
	KeyKeep         = "backup_keep"
	KeyKeepDays     = "backup_keep_days"
	KeySchedule     = "backup_schedule"
	DefaultSchedule = "daily 03:00"
	defaultKeep     = 5
	defaultKeepDays = 14
	archivePattern  = "atrium-*.backup"
	archiveStamp    = "20060102T150405Z"
	jobTimeout      = 2 * time.Hour
)

var stagedName = regexp.MustCompile(`^\.atrium-(snapshot|\d{8}T\d{6}Z\.backup)\.\d+(-wal|-shm|-journal)?$`)

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: KeyDirectory, Module: module.Core, Type: settings.String, Check: absoluteDir},
		{Key: KeyKeep, Module: module.Core, Type: settings.Int, Default: strconv.Itoa(defaultKeep), Check: settings.Between(1, 10000)},
		{Key: KeyKeepDays, Module: module.Core, Type: settings.Int, Default: strconv.Itoa(defaultKeepDays), Check: settings.Between(0, 36500)},
		{Key: KeySchedule, Module: module.Core, Type: settings.String, Default: DefaultSchedule},
	}
}

func absoluteDir(value string) error {
	if v := strings.TrimSpace(value); v != "" && !filepath.IsAbs(v) {
		return settings.Invalid("must be an absolute directory path")
	}
	return nil
}

func Job(env config.Env, create func(context.Context, string, func(context.Context, db.Querier) error) error) scheduler.Definition {
	if env == nil {
		env = config.OS
	}
	return scheduler.Definition{
		Module: module.Core, Key: JobKey, Name: passrun.Backup, Kind: scheduler.JobBuiltin,
		Prompt:   "Daily: write a verified backup archive to the backup directory, then prune old archives (no model run).",
		Schedule: DefaultSchedule, ScheduleHost: KeySchedule, Persistent: true, RunsWhilePaused: true, Timeout: jobTimeout,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, Resources: []string{passrun.Backup},
		Run: func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
			return scheduled(ctx, env, run.Claim, time.Now(), create)
		},
	}
}

func scheduled(ctx context.Context, env config.Env, claim scheduler.Claim, now time.Time, create func(context.Context, string, func(context.Context, db.Querier) error) error) (scheduler.Result, error) {
	dir := strings.TrimSpace(config.HostValueFrom(ctx, KeyDirectory))
	if dir == "" {
		return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new("no backup directory: set host key " + module.Core + "." + KeyDirectory)}, nil
	}
	keep := hostInt(ctx, KeyKeep, defaultKeep)
	keepDays := hostInt(ctx, KeyKeepDays, defaultKeepDays)
	cfg, err := config.Load(env)
	if err != nil {
		return scheduler.Result{}, err
	}
	key, err := dbkey.Load(env, settings.From(ctx))
	if err != nil {
		return scheduler.Result{}, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return scheduler.Result{}, err
	}
	defer func() { _ = d.Close() }()
	err = passrun.Track(ctx, d, passrun.Backup, func() error {
		if err := sweepStaged(dir, now.Add(-jobTimeout)); err != nil {
			return err
		}
		dest := filepath.Join(dir, "atrium-"+now.UTC().Format(archiveStamp)+".backup")
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("backup destination %s already exists", dest)
		}
		if err := create(ctx, dest, claim.RemoveFrom); err != nil {
			return err
		}
		return prune(dir, keep, keepDays, now)
	})
	if err != nil {
		return scheduler.Result{}, err
	}
	return scheduler.Result{Status: scheduler.StatusOK}, nil
}

func manual(ctx context.Context, d *db.DB, cfg config.Config, key db.Key, dest string) (int, error) {
	var count int
	var runErr error
	_, err := module.Invoke(ctx, d, cfg, JobKey, db.ShortLockWait, func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
		runErr = passrun.Track(ctx, d, passrun.Backup, func() error {
			var err error
			count, err = writeBackup(ctx, d, cfg, key, dest, run.Claim.RemoveFrom)
			return err
		})
		return scheduler.Result{}, runErr
	})
	return count, cmp.Or(err, runErr)
}

func hostInt(ctx context.Context, key string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(config.HostValueFrom(ctx, key)))
	if err != nil {
		return fallback
	}
	return n
}

func sweepStaged(dir string, before time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !stagedName.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.ModTime().Before(before) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func prune(dir string, keep, keepDays int, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type archive struct {
		path     string
		modified time.Time
	}
	var archives []archive
	for _, e := range entries {
		if matched, _ := filepath.Match(archivePattern, e.Name()); !matched || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		archives = append(archives, archive{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	slices.SortFunc(archives, func(a, b archive) int { return b.modified.Compare(a.modified) })
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	var errs []error
	for _, a := range archives[min(keep, len(archives)):] {
		if a.modified.Before(cutoff) {
			errs = append(errs, os.Remove(a.path))
		}
	}
	return errors.Join(errs...)
}

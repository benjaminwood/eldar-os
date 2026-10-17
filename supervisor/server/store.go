package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// writeJSONAtomic writes v as indented JSON via a temp file and rename.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Settings are Ben's preferences, persisted across restarts.
type Settings struct {
	path string
	mu   sync.Mutex
	v    api.Settings
	n    *Notifier
}

func NewSettings(path string, n *Notifier) *Settings {
	s := &Settings{path: path, n: n}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &s.v) != nil {
			logf("settings: unreadable %s, using defaults", path)
		}
	}
	return s
}

func (s *Settings) Get() api.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

// SetMobilePush changes and persists the setting; returns the new settings.
func (s *Settings) SetMobilePush(on bool) api.Settings {
	s.mu.Lock()
	s.v.MobilePush = on
	v := s.v
	if err := writeJSONAtomic(s.path, v); err != nil {
		logf("settings: save failed: %v", err)
	}
	s.mu.Unlock()
	s.n.Changed()
	return v
}

// Crons is the list of cron jobs allowed to auto-approve their own requests
// while they're running.
type Crons struct {
	path string
	mu   sync.Mutex
}

type cronFile struct {
	Crons []api.Cron `json:"crons"`
}

func NewCrons(path string) *Crons { return &Crons{path: path} }

func (c *Crons) List() []api.Cron {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read()
}

func (c *Crons) read() []api.Cron {
	var f cronFile
	b, err := os.ReadFile(c.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("crons: %v", err)
		}
		return []api.Cron{}
	}
	if json.Unmarshal(b, &f) != nil || f.Crons == nil {
		return []api.Cron{}
	}
	return f.Crons
}

func (c *Crons) Has(id string) bool {
	return slices.ContainsFunc(c.List(), func(x api.Cron) bool { return x.ID == id })
}

func (c *Crons) Add(id, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := slices.DeleteFunc(c.read(), func(x api.Cron) bool { return x.ID == id })
	list = append(list, api.Cron{ID: id, Name: name, AuthorizedAt: nowISO()})
	return writeJSONAtomic(c.path, cronFile{Crons: list})
}

func (c *Crons) Remove(id string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.read()
	kept := slices.DeleteFunc(slices.Clone(list), func(x api.Cron) bool { return x.ID == id })
	if len(kept) == len(list) {
		return false, nil
	}
	return true, writeJSONAtomic(c.path, cronFile{Crons: kept})
}

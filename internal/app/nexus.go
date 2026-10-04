package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

// NexusAccount is the person's linked Nexus Mods account. The API key is
// theirs: it is kept in their own config folder (readable only by them),
// only ever sent to api.nexusmods.com, and never shown again or logged.
type NexusAccount struct {
	Key     string    `json:"key"`
	User    string    `json:"user"`
	UserID  int       `json:"user_id"`
	Premium bool      `json:"premium"`
	Linked  time.Time `json:"linked"`
	Handler bool      `json:"handler,omitempty"`  // ppgmods handles nxm:// links
	PrevNXM string    `json:"prev_nxm,omitempty"` // the nxm handler before ppgmods (restored on unlink; gets other games' links)
	Checked time.Time `json:"checked,omitempty"`  // last time the key was validated
}

func nexusPath() string {
	d, err := manager.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "nexus.json")
}

// LoadNexus returns the linked account, or nil.
func LoadNexus() *NexusAccount {
	p := nexusPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var a NexusAccount
	if json.Unmarshal(b, &a) != nil || a.Key == "" {
		return nil
	}
	return &a
}

// Save writes the account, readable only by this user.
func (n *NexusAccount) Save() error {
	p := nexusPath()
	if p == "" {
		return fmt.Errorf("no config folder")
	}
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	os.Chmod(tmp, 0o600)
	return os.Rename(tmp, p)
}

// LinkNexus validates an API key and saves the account.
func LinkNexus(key string) (*NexusAccount, error) {
	u, err := sources.NXValidate(key)
	if err != nil {
		return nil, err
	}
	acct := &NexusAccount{Key: strings.TrimSpace(key), User: u.Name, UserID: u.ID, Premium: u.Premium, Linked: time.Now(), Checked: time.Now()}
	if old := LoadNexus(); old != nil {
		acct.Handler, acct.PrevNXM = old.Handler, old.PrevNXM
	}
	return acct, acct.Save()
}

// UnlinkNexus forgets the account.
func UnlinkNexus() error {
	p := nexusPath()
	if p == "" {
		return nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// fetchNexus downloads a Nexus file with the linked account: the newest main
// file (premium), or the file an nxm:// link names with its one-time key.
func (a *App) fetchNexus(modID int, link *sources.NXMLink) (*manager.Candidate, error) {
	dest, file, err := a.downloadNexus(modID, link)
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("nx:%d", modID)
	c, err := a.with(func(o *Options) { o.WorkshopID, o.Name = "", file.Name }).candidate(dest)
	if err != nil {
		return nil, err
	}
	c.Key, c.SteamOrig, c.Mirror = ref, false, fmt.Sprintf("nexus:%d", modID)
	c.Source, c.Version = fmt.Sprintf("https://www.nexusmods.com/peopleplayground/mods/%d", modID), file.Version
	c.Revision = time.Unix(file.Uploaded, 0) // Nexus uploads aren't reviewed: the cooldown applies
	if it, err := sources.NXGet(modID); err == nil {
		c.Name = it.Name
		// A Nexus upload of a Workshop item installs as that item.
		if in := sources.NXInfos([]sources.NXMod{*it})[modID]; in.WorkshopID != "" {
			c.Aliases = append(c.Aliases, ref)
			c.Key = "sky:" + in.WorkshopID
		}
	}
	return c, nil
}

// downloadNexus downloads a Nexus file into the cache with the linked
// account and returns its path.
func (a *App) downloadNexus(modID int, link *sources.NXMLink) (string, *sources.NXFile, error) {
	acct := LoadNexus()
	if acct == nil {
		return "", nil, fmt.Errorf("link your Nexus Mods account in Settings first")
	}
	files, err := sources.NXFiles(acct.Key, modID)
	if err != nil {
		return "", nil, err
	}
	var file *sources.NXFile
	if link != nil {
		for i := range files {
			if files[i].ID == link.FileID {
				file = &files[i]
			}
		}
		if file == nil {
			return "", nil, fmt.Errorf("Nexus Mods mod %d has no file %d", modID, link.FileID)
		}
	} else if file, err = sources.NXMainFile(acct.Key, modID); err != nil {
		return "", nil, err
	}
	nxmKey, expires := "", ""
	if link != nil {
		nxmKey, expires = link.Key, link.Expires
	}
	u, err := sources.NXDownloadURL(acct.Key, modID, file.ID, nxmKey, expires)
	if err != nil {
		return "", nil, err
	}
	dir, err := CacheDir(fmt.Sprintf("nx:%d", modID))
	if err != nil {
		return "", nil, err
	}
	ext := strings.ToLower(path.Ext(file.FileName))
	if ext != ".zip" && ext != ".rar" && ext != ".7z" {
		ext = ".zip" // the archive type is checked from its contents anyway
	}
	dest := filepath.Join(dir, fmt.Sprintf("nexus-%d%s", file.ID, ext))
	if _, err := os.Stat(dest); err != nil {
		a.logf("nx:%d %s - downloading %s (%d KB) from Nexus Mods", modID, file.Name, file.FileName, file.SizeKB)
		if _, _, err := sources.Download(u, dest+".part", 1<<30); err != nil {
			os.Remove(dest + ".part")
			return "", nil, err
		}
		if err := os.Rename(dest+".part", dest); err != nil {
			return "", nil, err
		}
	}
	return dest, file, nil
}

// InstallNXM installs the file a Nexus "Mod Manager Download" link names.
func (a *App) InstallNXM(m *manager.Manager, raw string) error {
	link, err := sources.ParseNXM(raw)
	if err != nil {
		return err
	}
	c, err := a.fetchNexus(link.ModID, link)
	if err != nil {
		return err
	}
	return a.installCandidate(m, c.Key, c)
}

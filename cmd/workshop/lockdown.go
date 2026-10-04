package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/DogeKingC/SWG/internal/manager"
)

// blocklistFile is blocklist/blocklist.json, in the order it is written.
type blocklistFile struct {
	Notes   string          `json:"notes"`
	Pause   []manager.Pause `json:"pause"`
	Entries []manager.Entry `json:"entries"`
}

// lockdown stops (on) or allows again (off) installs from every source in
// the app: it sets or clears the "pause" list of blocklist.json, which every
// copy of the app reads on its next action.
func lockdown(args []string, on bool) error {
	fs := flag.NewFlagSet("lockdown", flag.ExitOnError)
	file := fs.String("file", "blocklist/blocklist.json", "the blocklist to change")
	reason := fs.String("reason", "", "why (shown to everyone)")
	fs.Parse(args)
	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var bl blocklistFile
	if err := json.Unmarshal(b, &bl); err != nil {
		return fmt.Errorf("%s: %v", *file, err)
	}
	if on {
		why := strings.TrimSpace(*reason)
		if why == "" {
			return fmt.Errorf("pause-all needs a -reason")
		}
		if len(why) > 300 {
			why = why[:300]
		}
		bl.Pause = []manager.Pause{{Sources: []string{"*"}, Reason: why}}
		fmt.Println("installs from every source paused:", why)
	} else {
		bl.Pause = []manager.Pause{}
		fmt.Println("installs allowed again from every source")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(bl); err != nil {
		return err
	}
	return os.WriteFile(*file, out.Bytes(), 0o644)
}

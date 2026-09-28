package bklite_script

import (
	"errors"
	"fmt"
)

// checkRootPermission refuses to run a child as root unless allowRoot is set.
// euid is the Telegraf effective uid; username is the configured target user.
func checkRootPermission(euid int, username string, allowRoot bool) error {
	if allowRoot {
		return nil
	}
	targetRoot := username == "" || username == "root"
	if euid == 0 && targetRoot {
		return errors.New("refusing to run as root; set user to a non-root account or allow_root = true")
	}
	if username == "root" {
		return errors.New("refusing to run as root; set user to a non-root account or allow_root = true")
	}
	return nil
}

func wrapUserLookup(username string, err error) error {
	return fmt.Errorf("looking up user %q: %w", username, err)
}

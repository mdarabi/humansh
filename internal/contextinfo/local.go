package contextinfo

import (
	"os"
	"os/user"
	"path/filepath"
)

// Local inspects only fixed host metadata that the composition root explicitly
// injects into the provider-neutral app workflow.
type Local struct{}

func (Local) WorkingDirectoryLabel(mode, cwd string) string {
	if mode == "none" {
		return ""
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if mode == "full" {
		return cwd
	}
	home, homeErr := os.UserHomeDir()
	currentUser, userErr := user.Current()
	if homeErr != nil || userErr != nil || currentUser == nil || currentUser.Username == "" {
		return ""
	}
	base := filepath.Base(cwd)
	if cwd == home || base == currentUser.Username {
		return "~"
	}
	return base
}

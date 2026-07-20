package mow

import "github.com/subosito/mow/internal/config"

// Home returns the mow user data directory.
// Override with MOW_HOME (default ~/.mow). See config.Home for details.
func Home() string {
	return config.Home()
}

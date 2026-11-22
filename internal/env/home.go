package env

import "os"

// OSHome is the account's home directory. Some harnesses (Muse Code) clear
// the environment of the hooks they run, so a missing variable falls back to
// asking the OS.
func OSHome() (string, error) {
	if h, err := os.UserHomeDir(); err == nil {
		return h, nil
	}
	return accountHome()
}

package env

import "golang.org/x/sys/windows"

func accountHome() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Profile, windows.KF_FLAG_DEFAULT)
}

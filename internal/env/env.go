// Package env reads ainterlinked's environment variables. Each one is
// AINTERLINKED_<NAME>; the INTERLINKED_<NAME> spelling from before the
// rename is still read, so sessions started by an older binary (which set
// INTERLINKED_NAME, INTERLINKED_CWD, …) keep working until they restart.
package env

import "os"

// Prefix of every variable this program reads or sets.
const Prefix = "AINTERLINKED_"

// legacyPrefix is the spelling used before the rename.
const legacyPrefix = "INTERLINKED_"

// Get returns AINTERLINKED_<name>, or INTERLINKED_<name> when the new one is
// not set at all. Setting the new one to "" is an explicit "none" and hides
// the old spelling.
func Get(name string) string {
	if v, ok := os.LookupEnv(Prefix + name); ok {
		return v
	}
	return os.Getenv(legacyPrefix + name)
}

// Var is the name of the variable for name, as children should receive it.
func Var(name string) string { return Prefix + name }

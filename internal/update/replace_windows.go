package update

import "os"

// replace installs the new binary at exe. Windows can't overwrite a running
// executable but can rename it: the old one moves to exe.old (removed by
// RemoveOld once it no longer runs), and comes back if the move fails.
func replace(tmp, exe string) error {
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil && !os.IsNotExist(err) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	return nil
}

//go:build !unix

package update

// Exec can't replace a process here: `librepublish update` and a restart
// are the way.
func Exec(string) error { return ErrNoExec }

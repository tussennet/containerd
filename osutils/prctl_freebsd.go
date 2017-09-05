// +build freebsd

package osutils

import (
	"errors"
)

// GetSubreaper returns the subreaper setting for the calling process
func GetSubreaper() (int, error) {
	return 0, errors.New("osutils GetSubreaper not implemented on FreeBSD")
}

// SetSubreaper sets the value i as the subreaper setting for the calling process
func SetSubreaper(i int) error {
	return nil
}

package config

import "errors"

// ErrDirNotOurs is what EnsurePrivateDir returns, wrapped with the path and the
// two user ids, for a directory that belongs to somebody else.
//
// It is refused rather than repaired on purpose: whoever owns a directory can
// rename and replace what is in it whatever the modes of the things inside say,
// so a socket or a journal kept there would be at that user's mercy. chmod would
// fail for anyone but root, and for root it would mean quietly taking a
// directory away from its owner.
var ErrDirNotOurs = errors.New("directory belongs to another user")

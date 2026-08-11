package peer

// Image is the identity of an executable file, as the kernel reports it for a
// process that is running it: device and inode, never a path.
//
// The distinction is the whole point of this package. A path is text, and the
// file at a path belongs to whoever owns the directory -- so comparing paths,
// or comparing files found by path, is defeated by replacing one. Every Image
// here comes from asking the kernel what a given process is actually
// executing: /proc/<pid>/exe on linux, the vnode behind the process's own
// mapping of its main image on darwin.
//
// The fields are unexported so an Image can only be obtained from ImageOf.
// Nothing should be able to construct an identity out of two numbers it
// happened to have; an identity is something the kernel said.
//
// What an Image is NOT:
//
//   - Not code-signing identity. It says nothing about who signed a binary. A
//     rebuild is a different inode and therefore a different Image.
//   - Not a process identity. Two processes running the same file have the
//     same Image, which is exactly why the daemon also tracks which connection
//     opened which session.
//   - Not stable across a reinstall, and not necessarily stable across a
//     reboot: inode numbers survive, but device numbers can change when
//     filesystems are mounted differently. Anything that *persists* an Image
//     has to decide what to do when it stops matching -- see the enrollment
//     work that uses this, which must treat a mismatch as "not that agent"
//     rather than as an error, and must give the operator a way to re-enroll.
type Image struct {
	dev uint64
	ino uint64
}

// Dev and Ino expose the stored identity for callers that need to persist it.
// Comparison should go through Equal rather than these.
func (i Image) Dev() uint64 { return i.dev }
func (i Image) Ino() uint64 { return i.ino }

// IsZero reports whether this Image carries no identity at all, which is what
// a failed lookup leaves behind.
func (i Image) IsZero() bool { return i.dev == 0 && i.ino == 0 }

// Equal reports whether two Images are the same file.
//
// A zero Image is equal to nothing, including another zero Image. That is
// deliberate: every failure path in this package returns a zero value
// alongside its error, and a caller that forgets to check the error must not
// thereby discover that two unknowns match. An identity that was never
// established cannot be the same as anything.
func (i Image) Equal(other Image) bool {
	if i.IsZero() || other.IsZero() {
		return false
	}
	return i.dev == other.dev && i.ino == other.ino
}

// NewImage builds an Image from values the caller already holds -- only for
// rehydrating one that ImageOf produced earlier and something persisted.
//
// It is deliberately not a way to invent an identity: a zero pair yields a
// zero Image, which Equal never matches, so a row of missing columns cannot
// become an identity that compares equal to a running process.
func NewImage(dev, ino uint64) Image {
	if dev == 0 && ino == 0 {
		return Image{}
	}
	return Image{dev: dev, ino: ino}
}

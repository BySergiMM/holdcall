//go:build windows

package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/BySergiMM/nim/engine/internal/config"
)

// New returns a Store backed by DPAPI (CryptProtectData/CryptUnprotectData):
// the same primitive Windows itself uses for things like saved browser
// passwords. The encrypted blob for each target lives in a file under
// config.Home(), but the file's bytes are meaningless without the calling
// user's Windows login: DPAPI ties decryption to that, not to file
// permissions. This is deliberately the smaller of the two mechanisms named
// in the M3 decision (DPAPI, not the full Credential Manager API): its ABI
// surface is two structs and two calls rather than a whole struct-heavy
// CRED_* API, which matters because this cannot be run on real Windows in
// this environment -- it is cross-compiled and reviewed, not verified.
func New() (Store, error) {
	sum := sha256.Sum256([]byte(config.Home()))
	dir := filepath.Join(config.Home(), "credentials-"+hex.EncodeToString(sum[:4]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return windowsStore{dir: dir}, nil
}

type windowsStore struct{ dir string }

func (s windowsStore) path(target string) string {
	return filepath.Join(s.dir, target+".dpapi")
}

func (s windowsStore) Set(target, secret string) error {
	encrypted, err := dpapiProtect([]byte(secret))
	if err != nil {
		return fmt.Errorf("encrypting credential: %w", err)
	}
	if err := os.WriteFile(s.path(target), encrypted, 0o600); err != nil {
		return fmt.Errorf("writing credential: %w", err)
	}
	return nil
}

func (s windowsStore) Get(target string) (string, error) {
	encrypted, err := os.ReadFile(s.path(target))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("reading credential: %w", err)
	}
	plain, err := dpapiUnprotect(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting credential: %w", err)
	}
	return string(plain), nil
}

func (s windowsStore) Delete(target string) error {
	err := os.Remove(s.path(target))
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("removing credential: %w", err)
	}
	return nil
}

var (
	modcrypt32  = syscall.NewLazyDLL("crypt32.dll")
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCryptProtectData   = modcrypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = modcrypt32.NewProc("CryptUnprotectData")
	procLocalFree          = modkernel32.NewProc("LocalFree")
)

// dataBlob mirrors Win32's DATA_BLOB (wincrypt.h): a length and a pointer,
// the same layout on both sides of the syscall boundary.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newDataBlob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, int(b.cbData)))
	return out
}

// dpapiProtect encrypts plain for the current Windows user (CRYPTPROTECT_UI_FORBIDDEN
// is not set here deliberately -- see the flags argument below).
func dpapiProtect(plain []byte) ([]byte, error) {
	in := newDataBlob(plain)
	var out dataBlob
	// flags = 0: user-scoped (not CRYPTPROTECT_LOCAL_MACHINE), matching the
	// single-user-install model the rest of Nim already assumes.
	ok, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ok == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

func dpapiUnprotect(encrypted []byte) ([]byte, error) {
	in := newDataBlob(encrypted)
	var out dataBlob
	ok, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ok == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

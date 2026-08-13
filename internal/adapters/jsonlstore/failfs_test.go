package jsonlstore

import (
	"os"

	"github.com/spf13/afero"
)

// failFs wraps an afero.Fs and lets a test inject a failure into specific
// operations without relying on file permissions — this environment runs
// as root, so a chmod 0555 trick would not actually fail anything. Each
// hook is optional; a nil hook falls through to the wrapped Fs unchanged.
// No build tag: both the unit tier (wrapping afero.NewMemMapFs()) and the
// integration tier (wrapping afero.NewOsFs()) use this same type.
type failFs struct {
	afero.Fs

	OpenFileErr  func(name string, flag int, perm os.FileMode) error
	CreateErr    func(name string) error
	RenameErr    func(oldname, newname string) error
	MkdirAllErr  func(path string, perm os.FileMode) error
	RemoveAllErr func(path string) error
}

func (f *failFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if f.OpenFileErr != nil {
		if err := f.OpenFileErr(name, flag, perm); err != nil {
			return nil, err
		}
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f *failFs) Create(name string) (afero.File, error) {
	if f.CreateErr != nil {
		if err := f.CreateErr(name); err != nil {
			return nil, err
		}
	}
	return f.Fs.Create(name)
}

func (f *failFs) Rename(oldname, newname string) error {
	if f.RenameErr != nil {
		if err := f.RenameErr(oldname, newname); err != nil {
			return err
		}
	}
	return f.Fs.Rename(oldname, newname)
}

func (f *failFs) MkdirAll(path string, perm os.FileMode) error {
	if f.MkdirAllErr != nil {
		if err := f.MkdirAllErr(path, perm); err != nil {
			return err
		}
	}
	return f.Fs.MkdirAll(path, perm)
}

func (f *failFs) RemoveAll(path string) error {
	if f.RemoveAllErr != nil {
		if err := f.RemoveAllErr(path); err != nil {
			return err
		}
	}
	return f.Fs.RemoveAll(path)
}

// Interface conformance.
var _ afero.Fs = (*failFs)(nil)

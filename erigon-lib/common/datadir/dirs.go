// Copyright 2021 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/erigontech/erigon-lib/common/dir"
	"github.com/erigontech/erigon-lib/log/v3"
	"github.com/gofrs/flock"
)

// Dirs is the file system folder the node should use for any data storage
// requirements. The configured data directory will not be directly shared with
// registered services, instead those can use utility methods to create/access
// databases or flat files
type Dirs struct {
	DataDir          string
	RelativeDataDir  string // like dataDir, but without filepath.Abs() resolution
	Chaindata        string
	Tmp              string
	Snap             string
	SnapIdx          string
	SnapHistory      string
	SnapDomain       string
	SnapAccessors    string
	SnapCaplin       string
	Downloader       string
	TxPool           string
	Nodes            string
	CaplinBlobs      string
	CaplinColumnData string
	CaplinIndexing   string
	CaplinLatest     string
	CaplinGenesis    string
}

func New(datadir string) Dirs {
	dirs := Open(datadir)

	dir.MustExist(
		dirs.Chaindata,
		dirs.Tmp,
		dirs.SnapIdx,
		dirs.SnapHistory,
		dirs.SnapDomain,
		dirs.SnapAccessors,
		dirs.SnapCaplin,
		dirs.Downloader,
		dirs.TxPool,
		dirs.Nodes,
		dirs.CaplinBlobs,
		dirs.CaplinIndexing,
		dirs.CaplinLatest,
		dirs.CaplinGenesis,
		dirs.CaplinColumnData,
	)

	return dirs
}

// Create new Dirs instance without forcing all the directories to exist.
func Open(datadir string) Dirs {
	relativeDataDir := datadir
	if datadir != "" {
		var err error
		absdatadir, err := filepath.Abs(datadir)
		if err != nil {
			panic(err)
		}
		datadir = absdatadir
	}

	dirs := Dirs{
		RelativeDataDir:  relativeDataDir,
		DataDir:          datadir,
		Chaindata:        filepath.Join(datadir, "chaindata"),
		Tmp:              filepath.Join(datadir, "temp"),
		Snap:             filepath.Join(datadir, "snapshots"),
		SnapIdx:          filepath.Join(datadir, "snapshots", "idx"),
		SnapHistory:      filepath.Join(datadir, "snapshots", "history"),
		SnapDomain:       filepath.Join(datadir, "snapshots", "domain"),
		SnapAccessors:    filepath.Join(datadir, "snapshots", "accessor"),
		SnapCaplin:       filepath.Join(datadir, "snapshots", "caplin"),
		Downloader:       filepath.Join(datadir, "downloader"),
		TxPool:           filepath.Join(datadir, "txpool"),
		Nodes:            filepath.Join(datadir, "nodes"),
		CaplinBlobs:      filepath.Join(datadir, "caplin", "blobs"),
		CaplinColumnData: filepath.Join(datadir, "caplin", "column"),
		CaplinIndexing:   filepath.Join(datadir, "caplin", "indexing"),
		CaplinLatest:     filepath.Join(datadir, "caplin", "latest"),
		CaplinGenesis:    filepath.Join(datadir, "caplin", "genesis-state"),
	}
	return dirs
}

var (
	ErrDataDirLocked = errors.New("datadir already used by another process")

	datadirInUseErrNos = map[uint]bool{11: true, 32: true, 35: true}
)

func convertFileLockError(err error) error {
	//nolint
	if errno, ok := err.(syscall.Errno); ok && datadirInUseErrNos[uint(errno)] {
		return ErrDataDirLocked
	}
	return err
}

func TryFlock(dirs Dirs) (*flock.Flock, bool, error) {
	l := dirs.newFlock()
	locked, err := l.TryLock()
	if err != nil {
		return nil, false, convertFileLockError(err)
	}
	return l, locked, nil
}

// Dirs is huge, use pointer receiver to avoid copying it around. Returns a new flock.Flock for the
// datadir.
func (dirs *Dirs) newFlock() *flock.Flock {
	// Lock the instance directory to prevent concurrent use by another instance as well as
	// accidental use of the instance directory as a database.
	return flock.New(filepath.Join(dirs.DataDir, "LOCK"))
}

func (dirs Dirs) MustFlock() (Dirs, *flock.Flock, error) {
	l, locked, err := TryFlock(dirs)
	if err != nil {
		return dirs, l, err
	}
	if !locked {
		return dirs, l, ErrDataDirLocked
	}
	return dirs, l, nil
}

// Tries a non-blocking lock on the data directory. Converts failure to lock into ErrDataDirLocked.
// If err is nil, the unlock function must be called to release and close the flock.
func (dirs *Dirs) TryFlock() (unlock func(), err error) {
	f := dirs.newFlock()
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	locked, err := f.TryLock()
	if err != nil {
		err = convertFileLockError(err)
		return
	}
	if locked {
		unlock = func() {
			// If we fail to unlock the application is in a bad state (we can't recover from this).
			panicif.Err(f.Unlock())
		}
	} else {
		err = ErrDataDirLocked
	}
	return
}

// ApplyMigrations - if can get flock.
func ApplyMigrations(dirs Dirs) error { //nolint
	need, err := downloaderV2MigrationNeeded(dirs)
	if err != nil {
		return err
	}
	if !need {
		return nil
	}

	lock, locked, err := TryFlock(dirs)
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer lock.Unlock()

	// add your migration here

	if err := downloaderV2Migration(dirs); err != nil {
		return err
	}
	return nil
}

func downloaderV2MigrationNeeded(dirs Dirs) (bool, error) {
	return dir.FileExist(filepath.Join(dirs.Snap, "db", "mdbx.dat"))
}
func downloaderV2Migration(dirs Dirs) error {
	// move db from `datadir/snapshot/db` to `datadir/downloader`
	exists, err := downloaderV2MigrationNeeded(dirs)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	from, to := filepath.Join(dirs.Snap, "db", "mdbx.dat"), filepath.Join(dirs.Downloader, "mdbx.dat")
	if err := os.Rename(from, to); err != nil {
		//fall back to copy-file if folders are on different disks
		if err := CopyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

func CopyFile(from, to string) error {
	r, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("please manually move file: from %s to %s. error: %w", from, to, err)
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return fmt.Errorf("please manually move file: from %s to %s. error: %w", from, to, err)
	}
	defer w.Close()
	if _, err = w.ReadFrom(r); err != nil {
		w.Close()
		os.Remove(to)
		return fmt.Errorf("please manually move file: from %s to %s. error: %w", from, to, err)
	}
	if err = w.Sync(); err != nil {
		w.Close()
		os.Remove(to)
		return fmt.Errorf("please manually move file: from %s to %s. error: %w", from, to, err)
	}
	return nil
}

func (d Dirs) RenameOldVersions() error {
	directories := []string{
		d.Chaindata, d.Tmp, d.SnapIdx, d.SnapHistory, d.SnapDomain,
		d.SnapAccessors, d.SnapCaplin, d.Downloader, d.TxPool, d.Snap,
		d.Nodes, d.CaplinBlobs, d.CaplinIndexing, d.CaplinLatest, d.CaplinGenesis, d.CaplinColumnData,
	}
	renamed := 0
	torrentsRemoved := 0
	removed := 0
	for _, dirPath := range directories {
		err := filepath.WalkDir(dirPath, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if !entry.IsDir() {
				name := entry.Name()
				if strings.HasPrefix(name, "v1-") {
					if strings.HasSuffix(name, ".torrent") {
						if err := os.Remove(path); err != nil {
							return err
						}
						torrentsRemoved++
						return nil
					}

					if strings.Contains(entry.Name(), "commitment") &&
						(dirPath == d.SnapAccessors || dirPath == d.SnapHistory || dirPath == d.SnapIdx) {
						// remove the file instead of renaming
						if err := os.Remove(path); err != nil {
							return fmt.Errorf("failed to remove file %s: %w", path, err)
						}
						removed++
						return nil
					}

					newName := strings.Replace(name, "v1-", "v1.0-", 1)
					newPath := filepath.Join(filepath.Dir(path), newName)
					if err := os.Rename(path, newPath); err != nil {
						return err
					}
					renamed++
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	log.Info(fmt.Sprintf("Renamed %d directories to v1.0- and removed %d .torrent files", renamed, torrentsRemoved))
	if d.Downloader != "" && (renamed > 0 || removed > 0) {
		if err := os.RemoveAll(d.Downloader); err != nil {
			return err
		}
		log.Info(fmt.Sprintf("Removed Downloader directory: %s", d.Downloader))
	}

	return nil
}

func (d Dirs) RenameNewVersions() error {
	directories := []string{
		d.Chaindata, d.Tmp, d.SnapIdx, d.SnapHistory, d.SnapDomain,
		d.SnapAccessors, d.SnapCaplin, d.Downloader, d.TxPool, d.Snap,
		d.Nodes, d.CaplinBlobs, d.CaplinIndexing, d.CaplinLatest, d.CaplinGenesis, d.CaplinColumnData,
	}

	for _, dirPath := range directories {
		err := filepath.WalkDir(dirPath, func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if !dirEntry.IsDir() && strings.HasPrefix(dirEntry.Name(), "v1.0-") {
				if strings.Contains(dirEntry.Name(), "commitment") &&
					(dirPath == d.SnapAccessors || dirPath == d.SnapHistory || dirPath == d.SnapIdx) {
					// remove the file instead of renaming
					if err := os.Remove(path); err != nil {
						return fmt.Errorf("failed to remove file %s: %w", path, err)
					}
					return nil
				}
				newName := strings.Replace(dirEntry.Name(), "v1.0-", "v1-", 1)
				oldPath := path
				newPath := filepath.Join(filepath.Dir(path), newName)

				if err := os.Rename(oldPath, newPath); err != nil {
					return err
				}
			}
			return nil
		})

		if err != nil {
			return err
		}
	}

	return nil
}

func (d Dirs) PreverifiedPath() string {
	return filepath.Join(d.Snap, PreverifiedFileName)
}

const PreverifiedFileName = "preverified.toml"

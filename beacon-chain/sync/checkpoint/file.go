package checkpoint

import (
	"context"
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/io/file"
	log "github.com/sirupsen/logrus"
)

// Initializer describes a type that is able to obtain the checkpoint sync data (BeaconState and SignedBeaconBlock)
// in some way and perform database setup to prepare the beacon node for syncing from the given checkpoint.
// See FileInitializer and APIInitializer.
type Initializer interface {
	Initialize(ctx context.Context, d db.Database) error
}

// NewFileInitializer validates the given path information and creates an Initializer which will
// use the provided state and block files to prepare the node for checkpoint sync.
func NewFileInitializer(blockPath string, statePath string) (*FileInitializer, error) {
	var err error
	if err = existsAndIsFile(blockPath); err != nil {
		return nil, err
	}
	if err = existsAndIsFile(statePath); err != nil {
		return nil, err
	}
	// stat just to make sure it actually exists and is a file
	return &FileInitializer{blockPath: blockPath, statePath: statePath}, nil
}

// FileInitializer initializes a beacon-node database to use checkpoint sync,
// using ssz-encoded block and state data stored in files on the local filesystem.
// FileInitializer初始化一个beacon-node的db，使用checkpoint sync，使用ssz-encoded block以及
// 存储在文件中的state data，基于本地文件系统中的文件
type FileInitializer struct {
	blockPath string
	statePath string
}

// Initialize is called in the BeaconNode db startup code if an Initializer is present.
// Initialize does what is needed to prepare the beacon node database for syncing from the weak subjectivity checkpoint.
// Initialize在BeaconNode的db的启动代码中被调用，如果一个Initializer存在的话
// Initialize做需要做的工作来准备beacon node db，从weak subjectivity checkpoint中同步
func (fi *FileInitializer) Initialize(ctx context.Context, d db.Database) error {
	origin, err := d.OriginCheckpointBlockRoot(ctx)
	if err == nil && origin != params.BeaconConfig().ZeroHash {
		log.Warnf("origin checkpoint root %#x found in db, ignoring checkpoint sync flags", origin)
		return nil
	} else {
		if !errors.Is(err, db.ErrNotFound) {
			return errors.Wrap(err, "error while checking database for origin root")
		}
	}
	// 读取block文件
	serBlock, err := file.ReadFileAsBytes(fi.blockPath)
	if err != nil {
		return errors.Wrapf(err, "error reading block file %s for checkpoint sync init", fi.blockPath)
	}
	// 读取state文件
	serState, err := file.ReadFileAsBytes(fi.statePath)
	if err != nil {
		return errors.Wrapf(err, "error reading state file %s for checkpoint sync init", fi.blockPath)
	}
	return d.SaveOrigin(ctx, serState, serBlock)
}

var _ Initializer = &FileInitializer{}

func existsAndIsFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrapf(err, "error checking existence of ssz-encoded file %s for checkpoint sync init", path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, please specify full path to file", path)
	}
	return nil
}

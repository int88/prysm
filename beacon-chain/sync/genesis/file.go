package genesis

import (
	"context"
	"fmt"
	"os"

	"github.com/prysmaticlabs/prysm/io/file"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/beacon-chain/db"
)

// Initializer describes a type that is able to obtain the checkpoint sync data (BeaconState and SignedBeaconBlock)
// in some way and perform database setup to prepare the beacon node for syncing from the given checkpoint.
// See FileInitializer and APIInitializer.
// Initializer描述了一个类型，它可以以一些方式获取checkpoint sync data（BeaconState以及SignedBeaconBlock）
// 并且执行database的创建，来准备beacon node用于从给定的checkpoint同步
type Initializer interface {
	Initialize(ctx context.Context, d db.Database) error
}

// NewFileInitializer validates the given path information and creates an Initializer which will
// use the provided state and block files to prepare the node for checkpoint sync.
// NewFileInitializer校验给定的路径信息并且创建一个Initializer，它会使用提供的state以及block文件来准备
// node用于checkpoint sync
func NewFileInitializer(statePath string) (*FileInitializer, error) {
	var err error
	if err = existsAndIsFile(statePath); err != nil {
		return nil, err
	}
	// stat just to make sure it actually exists and is a file
	return &FileInitializer{statePath: statePath}, nil
}

// FileInitializer initializes a beacon-node database genesis state and block
// using ssz-encoded state data stored in files on the local filesystem.
// FileInitializer初始化一个beacon-node database genesis state以及block，使用ssz-encoded
// state data，存储在本地文件系统的文件中
type FileInitializer struct {
	statePath string
}

// Initialize is called in the BeaconNode db startup code if an Initializer is present.
// Initialize prepares the beacondb using the provided genesis state.
// Initialize在BeaconNode db启动代码的时候被调用，如果一个Initializer存在的话，Initialize准备
// beacondb，使用提供的genesis state
func (fi *FileInitializer) Initialize(ctx context.Context, d db.Database) error {
	serState, err := file.ReadFileAsBytes(fi.statePath)
	if err != nil {
		return errors.Wrapf(err, "error reading state file %s for checkpoint sync init", fi.statePath)
	}
	// 加载genesis
	return d.LoadGenesis(ctx, serState)
}

var _ Initializer = &FileInitializer{}

func existsAndIsFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrapf(err, "error checking existence of ssz-encoded file %s for genesis state init", path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, please specify full path to file", path)
	}
	return nil
}

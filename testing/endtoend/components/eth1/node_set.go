package eth1

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/testing/endtoend/helpers"
	e2e "github.com/prysmaticlabs/prysm/testing/endtoend/params"
	e2etypes "github.com/prysmaticlabs/prysm/testing/endtoend/types"
)

// NodeSet represents a set of Eth1 nodes, none of which is a mining node.
type NodeSet struct {
	e2etypes.ComponentRunner
	started chan struct{}
	enr     string
	nodes   []e2etypes.ComponentRunner
}

// NewNodeSet creates and returns a set of Eth1 nodes.
func NewNodeSet() *NodeSet {
	return &NodeSet{
		started: make(chan struct{}, 1),
	}
}

// SetMinerENR sets the miner's enode, used to connect to the miner through P2P.
// SetMinerENR设置miner的enode，用于通过P2P连接miner
func (s *NodeSet) SetMinerENR(enr string) {
	s.enr = enr
}

// Start starts all the beacon nodes in set.
// Start启动在set中的所有beacon nodes
func (s *NodeSet) Start(ctx context.Context) error {
	// Create Eth1 nodes. The number of nodes is the same as the number of beacon nodes.
	// We want each beacon node to connect to its own Eth1 node.
	// 我们想要每个beacon node都连接到它自己的Eth1 node
	// We start up one Eth1 node less than the beacon node count because the first
	// beacon node will connect to the already existing Eth1 miner.
	totalNodeCount := e2e.TestParams.BeaconNodeCount + e2e.TestParams.LighthouseBeaconNodeCount - 1
	nodes := make([]e2etypes.ComponentRunner, totalNodeCount)
	for i := 0; i < totalNodeCount; i++ {
		// We start indexing nodes from 1 because the miner has an implicit 0 index.
		// 构建nodes
		node := NewNode(i+1, s.enr)
		nodes[i] = node
	}
	s.nodes = nodes

	// Wait for all nodes to finish their job (blocking).
	// 等待所有的nodes结束他们的job（阻塞）
	// Once nodes are ready passed in handler function will be called.
	// 一旦nodes已经ready，传入的handler function会被调用
	return helpers.WaitOnNodes(ctx, nodes, func() {
		// All nodes started, close channel, so that all services waiting on a set, can proceed.
		// 所有的nodes都已经启动，关闭channel，这样所有等待set的services，可以开始处理
		close(s.started)
	})
}

// Started checks whether beacon node set is started and all nodes are ready to be queried.
func (s *NodeSet) Started() <-chan struct{} {
	return s.started
}

// Pause pauses the component and its underlying process.
func (s *NodeSet) Pause() error {
	for _, n := range s.nodes {
		if err := n.Pause(); err != nil {
			return err
		}
	}
	return nil
}

// Resume resumes the component and its underlying process.
func (s *NodeSet) Resume() error {
	for _, n := range s.nodes {
		if err := n.Resume(); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops the component and its underlying process.
func (s *NodeSet) Stop() error {
	for _, n := range s.nodes {
		if err := n.Stop(); err != nil {
			return err
		}
	}
	return nil
}

// PauseAtIndex pauses the component and its underlying process at the desired index.
func (s *NodeSet) PauseAtIndex(i int) error {
	if i >= len(s.nodes) {
		return errors.Errorf("provided index exceeds slice size: %d >= %d", i, len(s.nodes))
	}
	return s.nodes[i].Pause()
}

// ResumeAtIndex resumes the component and its underlying process at the desired index.
func (s *NodeSet) ResumeAtIndex(i int) error {
	if i >= len(s.nodes) {
		return errors.Errorf("provided index exceeds slice size: %d >= %d", i, len(s.nodes))
	}
	return s.nodes[i].Resume()
}

// StopAtIndex stops the component and its underlying process at the desired index.
func (s *NodeSet) StopAtIndex(i int) error {
	if i >= len(s.nodes) {
		return errors.Errorf("provided index exceeds slice size: %d >= %d", i, len(s.nodes))
	}
	return s.nodes[i].Stop()
}

// ComponentAtIndex returns the component at the provided index.
func (s *NodeSet) ComponentAtIndex(i int) (e2etypes.ComponentRunner, error) {
	if i >= len(s.nodes) {
		return nil, errors.Errorf("provided index exceeds slice size: %d >= %d", i, len(s.nodes))
	}
	return s.nodes[i], nil
}

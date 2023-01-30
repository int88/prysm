package depositsnapshot

const (
	DepositContractDepth = 32 // Maximum tree depth as defined by EIP-4881.
)

// MerkleTreeNode is the interface for a Merkle tree.
type MerkleTreeNode interface {
	// GetRoot returns the root of the Merkle tree.
	GetRoot() [32]byte
	// IsFull returns whether there is space left for deposits.
	// IsFull返回对于deposits是否还有空间
	IsFull() bool
	// Finalize marks deposits of the Merkle tree as finalized.
	// Finalize将Merkle tree的deposits标记为finalized
	Finalize(deposits uint, depth uint) MerkleTreeNode
	// GetFinalized returns a list of hashes of all the finalized nodes and the number of deposits.
	// GetFinalized返回一系列的哈希值，对于所有的finalized nodes以及deposits的数目
	GetFinalized(result [][32]byte) ([][32]byte, uint)
	// PushLeaf adds a new leaf node at the next available Zero node.
	// PushLeaf添加一个新的leaf node，在下一个可用的Zero node
	PushLeaf(leaf [32]byte, deposits uint, depth uint) MerkleTreeNode
}

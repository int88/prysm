package synccommittee

import (
	"github.com/pkg/errors"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/container/queue"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
)

// SaveSyncCommitteeMessage saves a sync committee message in to a priority queue.
// The priority queue capped at syncCommitteeMaxQueueSize contributions.
// SaveSyncCommitteeMessage保存一个sync committee message到一个优先级队列中
// 优先级队列限制为
func (s *Store) SaveSyncCommitteeMessage(msg *ethpb.SyncCommitteeMessage) error {
	if msg == nil {
		return errNilMessage
	}

	// 先上锁
	s.messageLock.Lock()
	defer s.messageLock.Unlock()

	item, err := s.messageCache.PopByKey(syncCommitteeKey(msg.Slot))
	if err != nil {
		return err
	}

	copied := ethpb.CopySyncCommitteeMessage(msg)
	// Messages exist in the queue. Append instead of insert new.
	// Messages在队列中存在，扩展而不是插入新的
	if item != nil {
		messages, ok := item.Value.([]*ethpb.SyncCommitteeMessage)
		if !ok {
			return errors.New("not typed []ethpb.SyncCommitteeMessage")
		}

		messages = append(messages, copied)
		savedSyncCommitteeMessageTotal.Inc()
		return s.messageCache.Push(&queue.Item{
			Key:      syncCommitteeKey(msg.Slot),
			Value:    messages,
			Priority: int64(msg.Slot),
		})
	}

	// Message does not exist. Insert new.
	// Message不存在，插入一个新的
	if err := s.messageCache.Push(&queue.Item{
		Key:      syncCommitteeKey(msg.Slot),
		Value:    []*ethpb.SyncCommitteeMessage{copied},
		Priority: int64(msg.Slot),
	}); err != nil {
		return err
	}
	savedSyncCommitteeMessageTotal.Inc()

	// Trim messages in queue down to syncCommitteeMaxQueueSize.
	// 剪裁队列里的messages到syncCommitteeMaxQueueSize
	if s.messageCache.Len() > syncCommitteeMaxQueueSize {
		if _, err := s.messageCache.Pop(); err != nil {
			return err
		}
	}

	return nil
}

// SyncCommitteeMessages returns sync committee messages by slot from the priority queue.
// Upon retrieval, the message is removed from the queue.
// SyncCommitteeMessages从priority queue返回sync committee messages，通过slot，在获取到之后
// message从队列中移除
func (s *Store) SyncCommitteeMessages(slot types.Slot) ([]*ethpb.SyncCommitteeMessage, error) {
	s.messageLock.RLock()
	defer s.messageLock.RUnlock()

	item := s.messageCache.RetrieveByKey(syncCommitteeKey(slot))
	if item == nil {
		return nil, nil
	}

	messages, ok := item.Value.([]*ethpb.SyncCommitteeMessage)
	if !ok {
		return nil, errors.New("not typed []ethpb.SyncCommitteeMessage")
	}

	return messages, nil
}

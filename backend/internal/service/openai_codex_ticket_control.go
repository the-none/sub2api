package service

import (
	"context"
	"errors"
	"sync"
)

// 一个实例内，策略写入、任务启动及票据发布共享顺序边界。
// 上游探测不持有此锁；最终持久化与策略写入需要互斥。
type codexTicketControl struct {
	mu     sync.RWMutex
	epoch  uint64
	cancel func(int64)
}

var errCodexTicketPolicyChanged = errors.New("ticket policy changed")

type codexTicketStamp struct {
	control *codexTicketControl
	epoch   uint64
}
type codexTicketStampKey struct{}

func (c *codexTicketControl) beginMutation(accountID int64) func() {
	c.mu.Lock()
	c.epoch++
	// Epoch is global within the instance: invalidate all snapshots and cancel
	// all obsolete workers consistently, including workers for other accounts.
	if c.cancel != nil {
		c.cancel(0)
	}
	return c.mu.Unlock
}

func (s *SettingService) beginTicketMutation() func() {
	if s == nil {
		return func() {}
	}
	return s.ticketControl.beginMutation(0)
}

func (s *OpenAIGatewayService) ticketCoordinator() *codexTicketControl {
	if s.settingService != nil {
		return &s.settingService.ticketControl
	}
	return &s.ticketControl
}

func (s *OpenAIGatewayService) stampTicketSnapshot(ctx context.Context) context.Context {
	control := s.ticketCoordinator()
	control.mu.RLock()
	stamp := codexTicketStamp{control: control, epoch: control.epoch}
	control.mu.RUnlock()
	return context.WithValue(ctx, codexTicketStampKey{}, stamp)
}

func guardTicketSnapshot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stamp, ok := ctx.Value(codexTicketStampKey{}).(codexTicketStamp)
	if !ok {
		return func() {}, nil
	}
	stamp.control.mu.RLock()
	if ctx.Err() != nil || stamp.control.epoch != stamp.epoch {
		stamp.control.mu.RUnlock()
		return nil, errCodexTicketPolicyChanged
	}
	return stamp.control.mu.RUnlock, nil
}

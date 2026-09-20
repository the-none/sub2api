package service

import (
	"context"
	"errors"
	"golang.org/x/sync/semaphore"
	"sync"
)

// 本实例策略写入、任务启动及票据发布共享顺序边界；等待读写许可可随请求取消。
// 上游探测不持有许可，只有策略写入和最终票据持久化需要互斥。
type codexTicketControl struct {
	once   sync.Once
	gate   *semaphore.Weighted
	epoch  uint64
	cancel func(int64)
}

const ticketWriteWeight int64 = 1 << 30

var errCodexTicketPolicyChanged = errors.New("ticket policy changed")

type codexTicketStamp struct {
	control *codexTicketControl
	epoch   uint64
}
type codexTicketStampKey struct{}

func (c *codexTicketControl) acquire(ctx context.Context, weight int64) (func(), error) {
	c.once.Do(func() { c.gate = semaphore.NewWeighted(ticketWriteWeight) })
	if err := c.gate.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		c.gate.Release(weight)
		return nil, err
	}
	return func() { c.gate.Release(weight) }, nil
}
func (c *codexTicketControl) beginMutation(ctx context.Context, _ int64) (func(), error) {
	release, err := c.acquire(ctx, ticketWriteWeight)
	if err != nil {
		return nil, err
	}
	c.epoch++
	// epoch 为实例级版本，因此一致取消本实例所有旧版本任务。
	if c.cancel != nil {
		c.cancel(0)
	}
	return release, nil
}
func (s *SettingService) beginTicketMutation(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	return s.ticketControl.beginMutation(ctx, 0)
}
func (s *OpenAIGatewayService) ticketCoordinator() *codexTicketControl {
	if s.settingService != nil {
		return &s.settingService.ticketControl
	}
	return &s.ticketControl
}
func (s *OpenAIGatewayService) stampTicketSnapshot(ctx context.Context) context.Context {
	control := s.ticketCoordinator()
	release, err := control.acquire(ctx, 1)
	if err != nil {
		return ctx
	}
	stamp := codexTicketStamp{control: control, epoch: control.epoch}
	release()
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
	release, err := stamp.control.acquire(ctx, 1)
	if err != nil {
		return nil, err
	}
	if stamp.control.epoch != stamp.epoch {
		release()
		return nil, errCodexTicketPolicyChanged
	}
	return release, nil
}

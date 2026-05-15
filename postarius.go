package postera

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidInput = errors.New("invalid input")

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

type Posterum struct {
	ID        string
	Message   string
	TriggerAt time.Time
	CreatedAt time.Time
}

type Postarius struct {
	registry Registry
	enqueuer Enqueuer
}

func New(registry Registry, enqueuer Enqueuer) *Postarius {
	return &Postarius{registry: registry, enqueuer: enqueuer}
}

func (p *Postarius) Create(ctx context.Context, message string, triggerAt time.Time) (Posterum, error) {
	if triggerAt.IsZero() {
		return Posterum{}, fmt.Errorf("postera: create: triggerAt must be non-zero: %w", ErrInvalidInput)
	}

	posterum := Posterum{
		ID:        generateID(),
		Message:   message,
		TriggerAt: triggerAt,
		CreatedAt: now(),
	}

	if err := p.enqueuer.Enqueue(ctx, posterum); err != nil {
		return Posterum{}, fmt.Errorf("postera: enqueue: %w", err)
	}

	if err := p.registry.Save(ctx, posterum); err != nil {
		rollback := p.enqueuer.Cancel(context.WithoutCancel(ctx), posterum.ID)
		if rollback != nil {
			return Posterum{}, errors.Join(
				fmt.Errorf("postera: create: %w", err),
				fmt.Errorf("postera: rollback cancel: %w", rollback),
			)
		}
		return Posterum{}, fmt.Errorf("postera: create: %w", err)
	}

	return posterum, nil
}

func (p *Postarius) Get(ctx context.Context, id string) (Posterum, error) {
	posterum, err := p.registry.Get(ctx, id)
	if err != nil {
		return Posterum{}, fmt.Errorf("postera: get: %w", err)
	}
	return posterum, nil
}

func (p *Postarius) Remove(ctx context.Context, id string) error {
	posterum, err := p.registry.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("postera: remove: %w", err)
	}

	if err := p.enqueuer.Cancel(ctx, id); err != nil {
		return fmt.Errorf("postera: cancel: %w", err)
	}

	if err := p.registry.Remove(context.WithoutCancel(ctx), id); err != nil {
		rollback := p.enqueuer.Enqueue(context.WithoutCancel(ctx), posterum)
		if rollback != nil {
			return errors.Join(
				fmt.Errorf("postera: remove: %w", err),
				fmt.Errorf("postera: rollback enqueue: %w", rollback),
			)
		}
		return fmt.Errorf("postera: remove: %w", err)
	}

	return nil
}

func (p *Postarius) List(ctx context.Context, q TimeRange) ([]Posterum, error) {
	entries, err := p.registry.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("postera: list: %w", err)
	}
	return entries, nil
}

func (p *Postarius) ListIncoming(ctx context.Context) ([]Posterum, error) {
	return p.List(ctx, TimeRange{From: now()})
}

func (p *Postarius) ListToday(ctx context.Context) ([]Posterum, error) {
	return p.ListByDate(ctx, now())
}

func (p *Postarius) ListIncomingToday(ctx context.Context) ([]Posterum, error) {
	t := now()
	_, end := dayBounds(t)
	return p.List(ctx, TimeRange{From: t, To: end})
}

func (p *Postarius) ListLastWeek(ctx context.Context) ([]Posterum, error) {
	return p.ListLastNDays(ctx, 7)
}

func (p *Postarius) ListLastNDays(ctx context.Context, n int) ([]Posterum, error) {
	if n < 0 {
		return nil, fmt.Errorf("postera: list last n days: n must be non-negative, got %d: %w", n, ErrInvalidInput)
	}
	t := now()
	return p.List(ctx, TimeRange{From: t.AddDate(0, 0, -n), To: t})
}

func (p *Postarius) ListByDate(ctx context.Context, date time.Time) ([]Posterum, error) {
	from, to := dayBounds(date)
	return p.List(ctx, TimeRange{From: from, To: to})
}

func dayBounds(t time.Time) (time.Time, time.Time) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return start, start.AddDate(0, 0, 1)
}

func now() time.Time {
	return time.Now().UTC()
}

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

const idPrefix = "pstr_"

func generateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("postera: crypto/rand failed: " + err.Error())
	}
	return idPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

type Posterum struct {
	ID        string
	Human     string
	Agent     string
	Session   string
	Metadata  map[string]string
	Message   string
	TriggerAt time.Time
	CreatedAt time.Time
}

type Postarius struct {
	store    Store
	enqueuer Enqueuer
}

func New(store Store, enqueuer Enqueuer) *Postarius {
	return &Postarius{store: store, enqueuer: enqueuer}
}

func (p *Postarius) Create(ctx context.Context, posterum Posterum) (Posterum, error) {
	if posterum.TriggerAt.IsZero() {
		return Posterum{}, fmt.Errorf("postera: create: triggerAt must be non-zero: %w", ErrInvalidInput)
	}

	posterum.ID = generateID()
	posterum.CreatedAt = time.Now().UTC()

	if err := p.enqueuer.Enqueue(ctx, posterum); err != nil {
		return Posterum{}, fmt.Errorf("postera: enqueue: %w", err)
	}

	if err := p.store.Save(ctx, posterum); err != nil {
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
	posterum, err := p.store.Get(ctx, id)
	if err != nil {
		return Posterum{}, fmt.Errorf("postera: get: %w", err)
	}
	return posterum, nil
}

func (p *Postarius) Remove(ctx context.Context, id string) error {
	posterum, err := p.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("postera: remove: %w", err)
	}

	if err := p.enqueuer.Cancel(ctx, id); err != nil {
		return fmt.Errorf("postera: cancel: %w", err)
	}

	if err := p.store.Remove(context.WithoutCancel(ctx), id); err != nil {
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

func (p *Postarius) List(ctx context.Context, q Query) ([]Posterum, error) {
	entries, err := p.store.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("postera: list: %w", err)
	}
	return entries, nil
}

// Package adk wraps agent.Tool as Google ADK function tools backed by
// functiontool.New. Identity is bridged automatically: UserID from the ADK
// tool context is extracted and injected as the postera namespace so that
// per-user data isolation is enforced without caller involvement.
package adk

import (
	"context"
	"errors"
	"fmt"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
)

// posterumView is the agent-facing representation of a Posterum.
// Body is a string because agents work with text; Posterum.Body is []byte
// internally and would be base64-encoded by ADK's JSON schema if left as-is.
// Time fields are RFC 3339 strings so that ADK's schema inference produces a
// human-readable type annotation rather than an opaque object.
type posterumView struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	ExecuteAt string `json:"execute_at"`
	CreatedAt string `json:"created_at"`
}

type createInput struct {
	Body      string `json:"body"`
	LocalTime string `json:"local_time"`
	Timezone  string `json:"timezone"`
}

type listInput struct {
	FromLocalTime string `json:"from_local_time"`
	ToLocalTime   string `json:"to_local_time"`
	Timezone      string `json:"timezone"`
}

type listByDateInput struct {
	LocalDate string `json:"local_date"`
	Timezone  string `json:"timezone"`
}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

// Tools holds the three ADK function tools for creating and querying Posterum
// entries. Use All() to register them with an llmagent in a single call.
type Tools struct {
	Create     adktool.Tool
	List       adktool.Tool
	ListByDate adktool.Tool
}

// All returns the tools in a slice suitable for passing to llmagent.New.
func (ts Tools) All() []adktool.Tool {
	return []adktool.Tool{ts.Create, ts.List, ts.ListByDate}
}

// New wraps t into Google ADK function tools. Each tool extracts the invoking
// user's identity via tool.Context.UserID() and injects it as the postera
// namespace before delegating to t, guaranteeing per-user data isolation.
//
// Note: tool.Context currently embeds context.Context via ReadonlyContext.
// Should a future ADK version decouple those types, only this file requires
// updating — agent.Tool is unaffected.
//
// Panics if t is nil.
func New(t *agent.Tool) (Tools, error) {
	if t == nil {
		panic("adk: New: t must not be nil")
	}

	create, err := functiontool.New(
		functiontool.Config{
			Name:        "create_posterum",
			Description: "Schedule a future reminder to be delivered at a specific local date and time. Provide the datetime in the user's local timezone as an ISO 8601 string without a timezone suffix (e.g. 2024-01-15T09:00:00) and the timezone as an IANA name (e.g. Asia/Jakarta).",
		},
		func(toolCtx adktool.Context, in createInput) (posterumView, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return posterumView{}, err
			}
			p, err := t.Create(ctx, agent.CreateArgs{
				Body:      []byte(in.Body),
				LocalTime: in.LocalTime,
				Timezone:  in.Timezone,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(p), nil
		},
	)
	if err != nil {
		return Tools{}, fmt.Errorf("adk: create tool: %w", err)
	}

	list, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum",
			Description: "List scheduled reminders within an optional time window. Leave from_local_time or to_local_time empty to leave that side unbounded. Provide datetime bounds in the user's local timezone as ISO 8601 strings without a timezone suffix (e.g. 2024-01-15T09:00:00) and the timezone as an IANA name (e.g. Asia/Jakarta).",
		},
		func(toolCtx adktool.Context, in listInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := t.List(ctx, agent.ListArgs{
				FromLocalTime: in.FromLocalTime,
				ToLocalTime:   in.ToLocalTime,
				Timezone:      in.Timezone,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries)}, nil
		},
	)
	if err != nil {
		return Tools{}, fmt.Errorf("adk: list tool: %w", err)
	}

	listByDate, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum_by_date",
			Description: "List all reminders scheduled on a specific calendar day in the user's local timezone. Day boundaries are computed in the given timezone, so 'today' reflects the user's locale rather than the server's UTC day. Provide the date as an ISO 8601 string (e.g. 2024-01-15) and the timezone as an IANA name (e.g. Asia/Jakarta).",
		},
		func(toolCtx adktool.Context, in listByDateInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := t.ListByDate(ctx, agent.ListByDateArgs{
				LocalDate: in.LocalDate,
				Timezone:  in.Timezone,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries)}, nil
		},
	)
	if err != nil {
		return Tools{}, fmt.Errorf("adk: list_by_date tool: %w", err)
	}

	return Tools{Create: create, List: list, ListByDate: listByDate}, nil
}

// contextWithNamespace extracts UserID from toolCtx and returns a
// context.Context carrying it as the postera namespace.
//
// toolCtx satisfies context.Context because tool.Context embeds
// agent.ReadonlyContext which itself embeds context.Context. It is used as
// the parent so that deadline and cancellation signals propagate correctly.
func contextWithNamespace(toolCtx adktool.Context) (context.Context, error) {
	userID := toolCtx.UserID()
	if userID == "" {
		return nil, errors.New("adk: unauthenticated: UserID is empty; ensure the agent is configured with a valid user session")
	}
	return postera.WithNamespace(toolCtx, userID), nil
}

func toPosterumView(p postera.Posterum) posterumView {
	return posterumView{
		ID:        p.ID,
		Body:      string(p.Body),
		ExecuteAt: p.ExecuteAt.UTC().Format(time.RFC3339),
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toPosterumViews(entries []postera.Posterum) []posterumView {
	views := make([]posterumView, len(entries))
	for i, e := range entries {
		views[i] = toPosterumView(e)
	}
	return views
}

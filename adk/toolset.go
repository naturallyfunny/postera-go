// Package adk wraps agent.ToolSet as Google ADK function tools backed by
// functiontool.New. Identity is bridged automatically: UserID from the ADK
// tool context is extracted and injected as the postera namespace so that
// per-user data isolation is enforced without caller involvement.
package adk

import (
	"context"
	"errors"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
)

// posterumView is the agent-facing representation of a Posterum. Time fields
// are naive ISO 8601 strings (no timezone suffix) localised to the user's
// timezone: the LLM reads "22:00:00" and reasons about it naturally without
// being exposed to UTC offsets or timezone identifiers.
type posterumView struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	RemindAt  string `json:"remind_at"`
	CreatedAt string `json:"created_at"`
}

// naiveTimeLayout formats a time.Time as a naive ISO 8601 datetime with no
// timezone suffix. Keeping the layout local to this file prevents accidental
// reuse with time.Parse (which would silently treat it as UTC).
const naiveTimeLayout = "2006-01-02T15:04:05"

type createInput struct {
	Message   string `json:"message"`
	LocalTime string `json:"local_time"`
}

type listInput struct {
	FromLocalTime string `json:"from_local_time"`
	ToLocalTime   string `json:"to_local_time"`
}

type listByDateInput struct {
	LocalDate string `json:"local_date"`
}

type listIncomingInput struct{}

type listTodayInput struct{}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

// Tools builds all five ADK function tools from ts and returns them as a
// slice. Returns an error if any tool fails to register.
func Tools(ts *agent.ToolSet) ([]adktool.Tool, error) {
	create, err := functiontool.New(
		functiontool.Config{
			Name:        "create_posterum",
			Description: "Schedule a future reminder for yourself at a specific local date and time. Provide the datetime in the user's local time as an ISO 8601 string without a timezone suffix (e.g. 2026-05-07T22:00:00). All times are automatically handled in the user's local timezone.",
		},
		func(toolCtx adktool.Context, in createInput) (posterumView, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return posterumView{}, err
			}
			p, err := ts.Create(ctx, agent.CreateArgs{
				Message:   in.Message,
				LocalTime: in.LocalTime,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(p, ts.LocationFromContext(ctx)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	list, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum",
			Description: "List scheduled reminders within an optional time window. Leave from_local_time or to_local_time empty to leave that side unbounded. Provide datetime bounds as ISO 8601 strings without a timezone suffix (e.g. 2024-01-15T09:00:00).",
		},
		func(toolCtx adktool.Context, in listInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.List(ctx, agent.ListArgs{
				FromLocalTime: in.FromLocalTime,
				ToLocalTime:   in.ToLocalTime,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listByDate, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum_by_date",
			Description: "List all reminders scheduled on a specific calendar day. Day boundaries are computed in the user's local timezone. Provide the date as an ISO 8601 string (e.g. 2024-01-15).",
		},
		func(toolCtx adktool.Context, in listByDateInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListByDate(ctx, agent.ListByDateArgs{
				LocalDate: in.LocalDate,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listIncoming, err := functiontool.New(
		functiontool.Config{
			Name:        "list_incoming_posterum",
			Description: "List all reminders that are scheduled to execute at or after the current instant. Use this to show the user what future reminders are pending.",
		},
		func(toolCtx adktool.Context, _ listIncomingInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListIncoming(ctx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listToday, err := functiontool.New(
		functiontool.Config{
			Name:        "list_today_posterum",
			Description: "List all reminders scheduled within the current calendar day in the user's local timezone, both past and future. Use this when the user asks what is on today's schedule.",
		},
		func(toolCtx adktool.Context, _ listTodayInput) (listOutput, error) {
			ctx, err := contextWithNamespace(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListToday(ctx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{create, list, listByDate, listIncoming, listToday}, nil
}

// contextWithNamespace extracts UserID from toolCtx and returns a
// context.Context carrying it as the postera namespace.
//
// toolCtx satisfies context.Context because tool.Context embeds
// agent.ReadonlyContext which itself embeds context.Context. It is used as
// the parent so that deadline, cancellation, and any values set by upstream
// middleware (e.g. timezone) propagate correctly into postera operations.
func contextWithNamespace(toolCtx adktool.Context) (context.Context, error) {
	userID := toolCtx.UserID()
	if userID == "" {
		return nil, errors.New("adk: unauthenticated: UserID is empty; ensure the agent is configured with a valid user session")
	}
	return postera.WithNamespace(toolCtx, userID), nil
}

// toPosterumView converts a Posterum to its agent-facing representation,
// formatting time fields in loc. loc must be non-nil; pass time.UTC when no
// user timezone is available. Time strings are naive ISO 8601 (no suffix) so
// the LLM reads them as local time without timezone noise.
func toPosterumView(p postera.Posterum, loc *time.Location) posterumView {
	return posterumView{
		ID:        p.ID,
		Message:   p.Message,
		RemindAt:  p.RemindAt.In(loc).Format(naiveTimeLayout),
		CreatedAt: p.CreatedAt.In(loc).Format(naiveTimeLayout),
	}
}

func toPosterumViews(entries []postera.Posterum, loc *time.Location) []posterumView {
	views := make([]posterumView, len(entries))
	for i, e := range entries {
		views[i] = toPosterumView(e, loc)
	}
	return views
}

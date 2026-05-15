package adk

import (
	"errors"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
)

const naiveTimeLayout = "2006-01-02T15:04:05"

type posterumView struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
	CreatedAt string `json:"created_at"`
}

type createArgs struct {
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
}

type listArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type listIncomingArgs struct{}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

func Tools(ts *agent.ToolSet) ([]adktool.Tool, error) {
	if ts == nil {
		return nil, errors.New("adk: Tools: ts must not be nil")
	}

	create, err := functiontool.New(
		functiontool.Config{
			Name:        "create_posterum",
			Description: "Schedule a future memory trigger for yourself at a specific date and time. Provide the datetime in the user's local time as an ISO 8601 string without a timezone suffix (e.g. 2026-05-07T22:00:00).",
		},
		func(toolCtx adktool.Context, in createArgs) (posterumView, error) {
			p, err := ts.Create(toolCtx, agent.CreateArgs{
				Message:   in.Message,
				TriggerAt: in.TriggerAt,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(p, ts.LocationFromContext(toolCtx)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	list, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum",
			Description: "List scheduled memory triggers within an optional time window. Leave from or to empty to leave that side unbounded. Provide datetime bounds as ISO 8601 strings without a timezone suffix (e.g. 2024-01-15T09:00:00).",
		},
		func(toolCtx adktool.Context, in listArgs) (listOutput, error) {
			entries, err := ts.List(toolCtx, agent.ListArgs{
				From: in.From,
				To:   in.To,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(toolCtx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listIncoming, err := functiontool.New(
		functiontool.Config{
			Name:        "list_incoming_posterum",
			Description: "List all memory triggers that are scheduled to execute at or after the current instant. Use this to show the user what future triggers are pending.",
		},
		func(toolCtx adktool.Context, _ listIncomingArgs) (listOutput, error) {
			entries, err := ts.ListIncoming(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(toolCtx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{create, list, listIncoming}, nil
}

func toPosterumView(p postera.Posterum, loc *time.Location) posterumView {
	return posterumView{
		ID:        p.ID,
		Message:   p.Message,
		TriggerAt: p.TriggerAt.In(loc).Format(naiveTimeLayout),
		CreatedAt: p.CreatedAt.In(loc).Format(naiveTimeLayout),
	}
}

func toPosterumViews(entries []postera.Posterum, loc *time.Location) []posterumView {
	views := make([]posterumView, len(entries))
	for i, entry := range entries {
		views[i] = toPosterumView(entry, loc)
	}
	return views
}

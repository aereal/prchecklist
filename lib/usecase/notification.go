package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/pkg/errors"

	"github.com/motemen/go-nuts/httputil"
	"github.com/motemen/prchecklist"
)

type eventType int

const (
	eventTypeInvalid eventType = iota
	eventTypeOnCheck
	eventTypeOnComplete
)

type notificationEvent interface {
	slackMessageText(ctx context.Context) string
	eventType() eventType
}

type addCheckEvent struct {
	checklist *prchecklist.Checklist
	item      *prchecklist.ChecklistItem
	user      prchecklist.GitHubUser
}

func (e addCheckEvent) slackMessageText(ctx context.Context) string {
	u := prchecklist.BuildURL(ctx, e.checklist.Path()).String()
	return fmt.Sprintf("[<%s|%s>] #%d %q checked by %s", u, e.checklist, e.item.Number, e.item.Title, e.user.Login)
}

func (e addCheckEvent) eventType() eventType { return eventTypeOnCheck }

type completeEvent struct {
	checklist *prchecklist.Checklist
}

func (e completeEvent) slackMessageText(ctx context.Context) string {
	u := prchecklist.BuildURL(ctx, e.checklist.Path()).String()
	return fmt.Sprintf("[<%s|%s>] Checklist completed! :tada:", u, e.checklist)
}

func (e completeEvent) eventType() eventType { return eventTypeOnComplete }

func (u Usecase) notifyEvent(ctx context.Context, checklist *prchecklist.Checklist, event notificationEvent) error {
	config := checklist.Config
	if config == nil {
		return nil
	}

	var chNames []string
	switch event.eventType() {
	case eventTypeOnCheck:
		chNames = config.Notification.Events.OnCheck
	case eventTypeOnComplete:
		chNames = config.Notification.Events.OnComplete
	default:
		return errors.Errorf("unknown event type: %v", event.eventType())
	}

	for _, name := range chNames {
		name := name
		ch, ok := config.Notification.Channels[name]
		if !ok {
			continue
		}

		go func() {
			payload, err := json.Marshal(&slackMessagePayload{
				Text: event.slackMessageText(ctx),
			})
			if err != nil {
				log.Printf("json.Marshal: %s", err)
				return
			}

			v := url.Values{"payload": {string(payload)}}

			_, err = httputil.Succeeding(http.PostForm(ch.URL, v))
			if err != nil {
				log.Printf("posting Slack webhook: %s", err)
				return
			}
		}()
	}

	return nil
}

type slackMessagePayload struct {
	Text string `json:"text"`
}

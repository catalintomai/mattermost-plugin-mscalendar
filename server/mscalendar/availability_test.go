// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See License for license information.

package mscalendar

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/mattermost/mattermost-server/v5/model"

	"github.com/mattermost/mattermost-plugin-mscalendar/server/config"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/mscalendar/mock_plugin_api"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/remote"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/remote/mock_remote"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/store"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/store/mock_store"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/utils/bot"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/utils/bot/mock_bot"
)

func TestSyncStatusAll(t *testing.T) {
	moment := time.Now().UTC()
	eventHash := "event_id " + moment.Format(time.RFC3339)
	busyEvent := &remote.Event{ID: "event_id", Start: remote.NewDateTime(moment, "UTC"), ShowAs: "busy"}

	for name, tc := range map[string]struct {
		remoteEvents  []*remote.Event
		activeEvents  []string
		currentStatus string
		newStatus     string
		eventsToStore []string
	}{
		"Most common case, no events local or remote. No status change.": {
			remoteEvents:  []*remote.Event{},
			activeEvents:  []string{},
			currentStatus: "online",
			newStatus:     "",
			eventsToStore: nil,
		},
		"New remote event. Change status to DND.": {
			remoteEvents:  []*remote.Event{busyEvent},
			activeEvents:  []string{},
			currentStatus: "online",
			newStatus:     "dnd",
			eventsToStore: []string{eventHash},
		},
		"Locally stored event is finished. Change status to online.": {
			remoteEvents:  []*remote.Event{},
			activeEvents:  []string{eventHash},
			currentStatus: "dnd",
			newStatus:     "online",
			eventsToStore: []string{},
		},
		"Locally stored event is still happening. No status change.": {
			remoteEvents:  []*remote.Event{busyEvent},
			activeEvents:  []string{eventHash},
			currentStatus: "dnd",
			newStatus:     "",
			eventsToStore: nil,
		},
		"User has manually set themselves to online during event. Locally stored event is still happening, but we will ignore it. No status change.": {
			remoteEvents:  []*remote.Event{busyEvent},
			activeEvents:  []string{eventHash},
			currentStatus: "online",
			newStatus:     "",
			eventsToStore: nil,
		},
		"Ignore non-busy event": {
			remoteEvents:  []*remote.Event{{ID: "event_id_2", Start: remote.NewDateTime(moment, "UTC"), ShowAs: "free"}},
			activeEvents:  []string{},
			currentStatus: "online",
			newStatus:     "",
			eventsToStore: nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			env, client := makeStatusSyncTestEnv(ctrl)
			deps := env.Dependencies

			c, papi, s := client.(*mock_remote.MockClient), deps.PluginAPI.(*mock_plugin_api.MockPluginAPI), deps.Store.(*mock_store.MockStore)

			s.EXPECT().LoadUser("user_mm_id").Return(&store.User{
				MattermostUserID: "user_mm_id",
				Remote: &remote.User{
					ID:   "user_remote_id",
					Mail: "user_email@example.com",
				},
				Settings:     store.Settings{UpdateStatus: true},
				ActiveEvents: tc.activeEvents,
			}, nil).Times(1)

			c.EXPECT().DoBatchViewCalendarRequests(gomock.Any()).Return([]*remote.ViewCalendarResponse{
				{Events: tc.remoteEvents, RemoteUserID: "user_remote_id"},
			}, nil)

			papi.EXPECT().GetMattermostUserStatusesByIds([]string{"user_mm_id"}).Return([]*model.Status{&model.Status{Status: tc.currentStatus, UserId: "user_mm_id"}}, nil)

			if tc.newStatus == "" {
				papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", gomock.Any()).Times(0)
			} else {
				papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", tc.newStatus).Return(nil, nil)
			}

			if tc.eventsToStore == nil {
				s.EXPECT().StoreUserActiveEvents("user_mm_id", gomock.Any()).Return(nil).Times(0)
			} else {
				s.EXPECT().StoreUserActiveEvents("user_mm_id", tc.eventsToStore).Return(nil).Times(1)
			}

			mscalendar := New(env, "")
			_, err := mscalendar.SyncStatusAll()
			require.Nil(t, err)
		})
	}
}

func TestSyncStatusUserConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		settings      store.Settings
		runAssertions func(deps *Dependencies, client remote.Client)
	}{
		"UpdateStatus disabled": {
			settings: store.Settings{
				UpdateStatus: false,
			},
			runAssertions: func(deps *Dependencies, client remote.Client) {
				c := client.(*mock_remote.MockClient)
				c.EXPECT().DoBatchViewCalendarRequests(gomock.Any()).Times(0)
			},
		},
		"GetConfirmation enabled": {
			settings: store.Settings{
				UpdateStatus:    true,
				GetConfirmation: true,
			},
			runAssertions: func(deps *Dependencies, client remote.Client) {
				c, papi, poster, s := client.(*mock_remote.MockClient), deps.PluginAPI.(*mock_plugin_api.MockPluginAPI), deps.Poster.(*mock_bot.MockPoster), deps.Store.(*mock_store.MockStore)
				moment := time.Now().UTC()
				busyEvent := &remote.Event{ID: "event_id", Start: remote.NewDateTime(moment, "UTC"), ShowAs: "busy"}

				c.EXPECT().DoBatchViewCalendarRequests(gomock.Any()).Times(1).Return([]*remote.ViewCalendarResponse{
					{Events: []*remote.Event{busyEvent}, RemoteUserID: "user_remote_id"},
				}, nil)
				papi.EXPECT().GetMattermostUserStatusesByIds([]string{"user_mm_id"}).Return([]*model.Status{&model.Status{Status: "online", UserId: "user_mm_id"}}, nil)

				s.EXPECT().StoreUserActiveEvents("user_mm_id", []string{"event_id " + moment.Format(time.RFC3339)})
				poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).Times(1)
				papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", gomock.Any()).Times(0)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			env, client := makeStatusSyncTestEnv(ctrl)

			s := env.Dependencies.Store.(*mock_store.MockStore)
			s.EXPECT().LoadUser("user_mm_id").Return(&store.User{
				MattermostUserID: "user_mm_id",
				Remote: &remote.User{
					ID:   "user_remote_id",
					Mail: "user_email@example.com",
				},
				Settings: tc.settings,
			}, nil).Times(1)

			tc.runAssertions(env.Dependencies, client)

			mscalendar := New(env, "")
			_, err := mscalendar.SyncStatusAll()
			require.Nil(t, err)
		})
	}
}

func makeStatusSyncTestEnv(ctrl *gomock.Controller) (Env, remote.Client) {
	s := mock_store.NewMockStore(ctrl)
	poster := mock_bot.NewMockPoster(ctrl)
	mockRemote := mock_remote.NewMockRemote(ctrl)
	mockClient := mock_remote.NewMockClient(ctrl)
	mockPluginAPI := mock_plugin_api.NewMockPluginAPI(ctrl)

	logger := &bot.NilLogger{}
	conf := &config.Config{BotUserID: "bot_mm_id"}
	env := Env{
		Config: conf,
		Dependencies: &Dependencies{
			Store:             s,
			Logger:            logger,
			Poster:            poster,
			Remote:            mockRemote,
			PluginAPI:         mockPluginAPI,
			IsAuthorizedAdmin: func(mattermostUserID string) (bool, error) { return true, nil },
		},
	}

	s.EXPECT().LoadUserIndex().Return(store.UserIndex{
		&store.UserShort{
			MattermostUserID: "user_mm_id",
			RemoteID:         "user_remote_id",
			Email:            "user_email@example.com",
		},
	}, nil).Times(1)

	token := &oauth2.Token{
		AccessToken: "bot_oauth_token",
	}
	s.EXPECT().LoadUser("bot_mm_id").Return(&store.User{
		MattermostUserID: "bot_mm_id",
		OAuth2Token:      token,
		Remote: &remote.User{
			ID:   "bot_remote_id",
			Mail: "bot_email@example.com",
		},
	}, nil).Times(1)

	mockPluginAPI.EXPECT().GetMattermostUser("bot_mm_id").Return(&model.User{}, nil).Times(1)

	mockRemote.EXPECT().MakeClient(context.Background(), token).Return(mockClient).Times(1)
	mockClient.EXPECT().GetSuperuserToken().Return("bot_bearer_token", nil)
	mockRemote.EXPECT().MakeSuperuserClient(context.Background(), "bot_bearer_token").Return(mockClient)

	return env, mockClient
}

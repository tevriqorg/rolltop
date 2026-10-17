package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"rolltop/backend/mailai"
	"rolltop/backend/provider"
	"rolltop/backend/store"
)

// runMailAIActionTool exposes Rolltop's real mail business capabilities to the
// Mail AI bridge. The shared-token endpoint remains tenant-scoped by user_id,
// while every state-changing or externally-visible action must also carry the
// confirmation bit supplied by the Agent approval flow. Draft creation is the
// deliberate exception: it persists a draft but never sends mail.
func (s *Server) runMailAIActionTool(r *http.Request, in mailai.ToolRequest) (mailai.ToolResponse, int, error) {
	ctx := r.Context()
	result := func(value any) (mailai.ToolResponse, int, error) {
		return mailai.ToolResponse{Name: in.Name, Result: value}, http.StatusOK, nil
	}
	requireConfirmed := func() (mailai.ToolResponse, int, error) {
		if !in.Confirmed {
			return mailai.ToolResponse{}, http.StatusConflict, errors.New("explicit confirmation required")
		}
		return mailai.ToolResponse{}, 0, nil
	}

	switch in.Name {
	case "mark_read":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64 `json:"message_id"`
			Read      bool  `json:"read"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("mark_read requires message_id")
		}
		owned, err := s.store.OwnedMessageIDsForUser(ctx, in.UserID, []int64{args.MessageID})
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		if len(owned) != 1 {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		realIDs, err := s.markMailRead(ctx, in.UserID, owned, args.Read)
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		s.notifyUserChanged(in.UserID)
		if s.syncer != nil && len(realIDs) > 0 {
			s.queueReadStatePush(in.UserID, append([]int64(nil), realIDs...))
		}
		return result(map[string]any{"ok": true, "message_id": args.MessageID, "read": args.Read})

	case "archive_message":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("archive_message requires message_id")
		}
		if mock, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: "archive", Value: true}); err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		} else if mock {
			s.noteMailListChanged(in.UserID)
			return result(map[string]any{"ok": true, "message_id": args.MessageID, "archived": true})
		}
		msg, source, err := s.mailAIMessageMailbox(ctx, in.UserID, args.MessageID)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		prefs, err := s.store.GetSwipePreferences(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		var mailboxID int64
		for _, target := range prefs.ArchiveMailboxes {
			if target.AccountID == source.AccountID {
				mailboxID = target.MailboxID
				break
			}
		}
		if mailboxID <= 0 {
			return mailai.ToolResponse{}, http.StatusConflict, errors.New("choose an Archive folder for this account in swipe settings")
		}
		dest, err := s.store.GetMailboxForUser(ctx, in.UserID, mailboxID)
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		if err := s.mailAIMoveMessage(ctx, in.UserID, msg.ID, dest); err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		return result(map[string]any{"ok": true, "message_id": msg.ID, "mailbox_id": dest.ID, "mailbox": dest.Name, "archived": true})

	case "list_mailboxes":
		mailboxes, err := s.store.ListMailboxesForUser(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		items := make([]map[string]any, 0, len(mailboxes))
		for _, mailbox := range mailboxes {
			items = append(items, map[string]any{
				"mailbox_id": mailbox.ID,
				"account_id": mailbox.AccountID,
				"account":    mailAIFirstNonEmpty(mailbox.AccountLabel, mailbox.AccountEmail),
				"name":       mailbox.Name,
				"role":       mailbox.Role,
			})
		}
		return result(items)

	case "star", "unstar":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("message_id is required")
		}
		starred := in.Name == "star"
		if mock, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: "star", Value: starred}); err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		} else if mock {
			s.noteMailListChanged(in.UserID)
			return result(map[string]any{"ok": true, "message_id": args.MessageID, "starred": starred})
		}
		if s.syncer == nil {
			return mailai.ToolResponse{}, http.StatusServiceUnavailable, errors.New("IMAP sync is not configured")
		}
		msg, err := s.syncer.SetStarredForMessage(ctx, in.UserID, args.MessageID, starred)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusBadGateway, err
		}
		go func(userID, messageID int64) {
			background := s.backgroundContext()
			_ = s.syncer.SyncStarStateForMessage(background, userID, messageID)
			s.notifyUserChanged(userID)
		}(in.UserID, msg.ID)
		s.notifyUserChanged(in.UserID)
		return result(map[string]any{"ok": true, "message_id": msg.ID, "starred": starred})

	case "move":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64  `json:"message_id"`
			MailboxID int64  `json:"mailbox_id"`
			Mailbox   string `json:"mailbox"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 || (args.MailboxID <= 0 && strings.TrimSpace(args.Mailbox) == "") {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("move requires message_id and mailbox or mailbox_id")
		}
		if _, err := s.providerMessage(ctx, in.UserID, args.MessageID); err == nil {
			role := strings.ToLower(strings.TrimSpace(args.Mailbox))
			switch role {
			case "trash", "垃圾箱", "废纸篓":
				if _, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: "trash", Value: true}); err != nil {
					return mailai.ToolResponse{}, http.StatusInternalServerError, err
				}
			case "inbox", "收件箱":
				if _, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: "restore", Value: true}); err != nil {
					return mailai.ToolResponse{}, http.StatusInternalServerError, err
				}
			case "archive", "all", "归档":
				if _, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: "archive", Value: true}); err != nil {
					return mailai.ToolResponse{}, http.StatusInternalServerError, err
				}
			default:
				return mailai.ToolResponse{}, http.StatusConflict, errors.New("synthetic provider does not support arbitrary folder moves")
			}
			s.noteMailListChanged(in.UserID)
			return result(map[string]any{"ok": true, "message_id": args.MessageID, "mailbox": strings.TrimSpace(args.Mailbox)})
		} else if !store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		dest, err := s.mailAIResolveDestinationMailbox(ctx, in.UserID, args.MessageID, args.MailboxID, args.Mailbox)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("destination mailbox not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusBadRequest, err
		}
		if err := s.mailAIMoveMessage(ctx, in.UserID, args.MessageID, dest); err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		return result(map[string]any{"ok": true, "message_id": args.MessageID, "mailbox_id": dest.ID, "mailbox": dest.Name})

	case "snooze":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64  `json:"message_id"`
			Until     string `json:"until"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("snooze requires message_id and until")
		}
		until, err := parseSnoozeUntil(args.Until, 0, time.Now().UTC())
		if err != nil {
			return mailai.ToolResponse{}, http.StatusBadRequest, err
		}
		snooze, err := s.store.SnoozeMessage(ctx, in.UserID, args.MessageID, until)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		s.notifySnoozeStateChanged(in.UserID)
		return result(map[string]any{"ok": true, "message_id": args.MessageID, "snoozed_until": snooze.SnoozedUntil.UTC().Format(time.RFC3339)})

	case "trash", "restore":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("message_id is required")
		}
		trash := in.Name == "trash"
		if mock, err := s.mutateProviderMessage(ctx, in.UserID, args.MessageID, provider.Mutation{Action: map[bool]string{true: "trash", false: "restore"}[trash], Value: true}); err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		} else if mock {
			s.noteMailListChanged(in.UserID)
			return result(map[string]any{"ok": true, "message_id": args.MessageID, "trashed": trash})
		}
		targetRole := "trash"
		if !trash {
			targetRole = "inbox"
		}
		msg, source, err := s.mailAIMessageMailbox(ctx, in.UserID, args.MessageID)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		dest, err := s.store.GetMailboxByRoleForAccount(ctx, in.UserID, source.AccountID, targetRole)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusConflict, fmt.Errorf("account has no %s mailbox", targetRole)
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		if err := s.mailAIMoveMessage(ctx, in.UserID, msg.ID, dest); err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		return result(map[string]any{"ok": true, "message_id": msg.ID, "mailbox_id": dest.ID, "mailbox": dest.Name, "trashed": trash})

	case "create_draft":
		var args struct {
			To             string `json:"to"`
			Cc             string `json:"cc"`
			Bcc            string `json:"bcc"`
			Subject        string `json:"subject"`
			Body           string `json:"body"`
			FromIdentityID int64  `json:"from_identity_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || strings.TrimSpace(args.Body) == "" {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("create_draft requires body")
		}
		cu, err := s.mailAICurrentUser(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		draft, err := s.saveComposeDraft(ctx, cu, composeForm{To: strings.TrimSpace(args.To), Cc: strings.TrimSpace(args.Cc), Bcc: strings.TrimSpace(args.Bcc), Subject: strings.TrimSpace(args.Subject), Body: strings.TrimSpace(args.Body), FromIdentityID: args.FromIdentityID})
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		s.notifyUserChanged(in.UserID)
		return result(map[string]any{"ok": true, "message_id": draft.ID, "to": args.To, "subject": args.Subject, "body": args.Body, "send": false})

	case "reply_mail":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID int64  `json:"message_id"`
			Body      string `json:"body"`
			ReplyAll  bool   `json:"reply_all"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 || strings.TrimSpace(args.Body) == "" {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("reply_mail requires message_id and body")
		}
		cu, err := s.mailAICurrentUser(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		msg, err := s.store.GetMessageForUser(ctx, in.UserID, args.MessageID)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		thread, err := s.store.ListThreadMessagesForUser(ctx, in.UserID, msg)
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		var form composeForm
		if args.ReplyAll {
			form = s.replyAllComposeFormForMessage(ctx, cu, msg, thread, s.ownAddresses(ctx, cu.User))
		} else {
			form = s.replyComposeFormForMessage(ctx, cu, msg, thread, s.ownAddresses(ctx, cu.User))
		}
		s.applyReplyComposeDefaults(ctx, cu, msg, thread, &form)
		mailAIPrependPlainBody(&form, args.Body)
		return s.mailAIQueueCompose(ctx, in, cu, form)

	case "forward_mail":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			MessageID          int64  `json:"message_id"`
			To                 string `json:"to"`
			Cc                 string `json:"cc"`
			Bcc                string `json:"bcc"`
			Body               string `json:"body"`
			IncludeAttachments bool   `json:"include_attachments"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 || strings.TrimSpace(args.To) == "" {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("forward_mail requires message_id and to")
		}
		cu, err := s.mailAICurrentUser(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		msg, err := s.store.GetMessageForUser(ctx, in.UserID, args.MessageID)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, http.StatusNotFound, errors.New("message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, http.StatusInternalServerError, err
		}
		form := s.forwardComposeFormForMessage(ctx, in.UserID, msg)
		form.To, form.Cc, form.Bcc = strings.TrimSpace(args.To), strings.TrimSpace(args.Cc), strings.TrimSpace(args.Bcc)
		if strings.TrimSpace(args.Body) != "" {
			mailAIPrependPlainBody(&form, args.Body)
		}
		if args.IncludeAttachments {
			attachments, err := s.composeExistingAttachmentsForMessage(ctx, in.UserID, msg.ID)
			if err != nil {
				return mailai.ToolResponse{}, http.StatusInternalServerError, err
			}
			form.IncludeAttachmentIDs = composeExistingAttachmentIDs(attachments)
		}
		return s.mailAIQueueCompose(ctx, in, cu, form)

	case "send_mail":
		if response, status, err := requireConfirmed(); err != nil {
			return response, status, err
		}
		var args struct {
			To             string `json:"to"`
			Cc             string `json:"cc"`
			Bcc            string `json:"bcc"`
			Subject        string `json:"subject"`
			Body           string `json:"body"`
			FromIdentityID int64  `json:"from_identity_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || strings.TrimSpace(args.To) == "" || strings.TrimSpace(args.Body) == "" {
			return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("send_mail requires to and body")
		}
		cu, err := s.mailAICurrentUser(ctx, in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, mailAIActionStatus(err), err
		}
		return s.mailAIQueueCompose(ctx, in, cu, composeForm{To: strings.TrimSpace(args.To), Cc: strings.TrimSpace(args.Cc), Bcc: strings.TrimSpace(args.Bcc), Subject: strings.TrimSpace(args.Subject), Body: strings.TrimSpace(args.Body), FromIdentityID: args.FromIdentityID})
	default:
		return mailai.ToolResponse{}, http.StatusBadRequest, errors.New("unsupported mail-ai action tool")
	}
}

func mailAIFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) mailAICurrentUser(ctx context.Context, userID int64) (currentUser, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return currentUser{}, err
	}
	return currentUser{User: user}, nil
}

func (s *Server) mailAIMessageMailbox(ctx context.Context, userID, messageID int64) (store.MessageRecord, store.Mailbox, error) {
	msg, err := s.store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return store.MessageRecord{}, store.Mailbox{}, err
	}
	mailbox, err := s.store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	return msg, mailbox, err
}

func (s *Server) mailAIResolveDestinationMailbox(ctx context.Context, userID, messageID, mailboxID int64, mailboxName string) (store.Mailbox, error) {
	_, source, err := s.mailAIMessageMailbox(ctx, userID, messageID)
	if err != nil {
		return store.Mailbox{}, err
	}
	if mailboxID > 0 {
		dest, err := s.store.GetMailboxForUser(ctx, userID, mailboxID)
		if err != nil {
			return store.Mailbox{}, err
		}
		if dest.AccountID != source.AccountID {
			return store.Mailbox{}, errors.New("move destination must belong to the same account")
		}
		return dest, nil
	}
	want := strings.TrimSpace(mailboxName)
	mailboxes, err := s.store.ListMailboxesForAccount(ctx, userID, source.AccountID)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mailbox := range mailboxes {
		if strings.EqualFold(mailbox.Name, want) {
			return mailbox, nil
		}
	}
	for _, mailbox := range mailboxes {
		if mailbox.Role != "" && strings.EqualFold(mailbox.Role, want) {
			return mailbox, nil
		}
	}
	return store.Mailbox{}, store.ErrNotFound
}

func (s *Server) mailAIMoveMessage(ctx context.Context, userID, messageID int64, dest store.Mailbox) error {
	if s.syncer == nil {
		return errors.New("IMAP sync is not configured")
	}
	refresh, err := s.moveRefreshMailboxNames(ctx, userID, []int64{messageID}, dest)
	if err != nil {
		return err
	}
	finish := func() {}
	if s.syncRunner != nil {
		finish, err = s.syncRunner.BeginForegroundOperation(ctx, userID)
		if err != nil {
			return fmt.Errorf("could not schedule message move: %w", err)
		}
	}
	defer finish()
	if err := s.syncer.MoveMessage(ctx, userID, messageID, dest.ID); err != nil {
		return err
	}
	s.startMoveRefresh(userID, dest.AccountID, refresh)
	finish()
	return nil
}

func mailAIPrependPlainBody(form *composeForm, body string) {
	body = strings.TrimSpace(body)
	quoted := strings.TrimSpace(form.Body)
	if quoted != "" {
		form.Body = body + "\n\n" + quoted
	} else {
		form.Body = body
	}
	// Keep AI-authored content plain text. This avoids treating model output as
	// trusted HTML while preserving the server-owned quoted text.
	form.BodyHTML = ""
}

func (s *Server) mailAIQueueCompose(ctx context.Context, in mailai.ToolRequest, cu currentUser, form composeForm) (mailai.ToolResponse, int, error) {
	job, sent, err := s.queueCompose(ctx, cu, form)
	if err != nil {
		return mailai.ToolResponse{}, mailAIActionStatus(err), err
	}
	s.notifyOutboxChanged(in.UserID)
	s.wakeOutboxWorker()
	return mailai.ToolResponse{Name: in.Name, Result: map[string]any{
		"ok": true, "queued": true, "send_id": job.ID, "message_id": sent.ID,
		"status": job.DeliveryState, "to": form.To, "subject": form.Subject,
	}}, http.StatusOK, nil
}

func mailAIActionStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if store.IsNotFound(err) {
		return http.StatusNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not configured") || strings.Contains(message, "does not support") || strings.Contains(message, "no ") && strings.Contains(message, "mailbox") {
		return http.StatusServiceUnavailable
	}
	if strings.Contains(message, "required") || strings.Contains(message, "invalid") || strings.Contains(message, "recipient") || strings.Contains(message, "identity") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

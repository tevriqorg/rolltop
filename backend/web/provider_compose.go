package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"rolltop/backend/provider"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
)

// providerComposeConnection resolves a provider-native outgoing identity. A
// reply/forward to a provider-managed message is sticky to that source account;
// otherwise an explicitly/implicitly selected From identity may match a Google
// connection. A locked Gmail account never silently falls back to SMTP.
func (s *Server) providerComposeConnection(ctx context.Context, cu currentUser, form composeForm, identity composeIdentity) (store.AccountConnection, bool, error) {
	sourceMessageID := form.ProviderSourceMessageID
	if sourceMessageID <= 0 {
		sourceMessageID = form.InReplyToID
	}
	if sourceMessageID > 0 {
		meta, err := s.store.ProviderMessageForLocalID(ctx, cu.User.ID, sourceMessageID)
		if err == nil && meta.Provider == string(provider.Gmail) {
			connection, err := s.googleConnectionForSource(ctx, cu.User.ID, meta.Account)
			if err != nil {
				return store.AccountConnection{}, true, err
			}
			gate, err := s.store.GetProviderSyncGate(ctx, cu.User.ID, string(provider.Gmail), meta.Account)
			if err != nil {
				return store.AccountConnection{}, true, err
			}
			if !gate.BackfillApproved() {
				return store.AccountConnection{}, true, errors.New("Gmail send remains locked pending Census review and explicit approval")
			}
			return connection, true, nil
		} else if err != nil && !store.IsNotFound(err) {
			return store.AccountConnection{}, false, err
		}
	}
	if s.googleConnector == nil || strings.TrimSpace(identity.Email) == "" {
		return store.AccountConnection{}, false, nil
	}
	connections, err := s.store.ListAccountConnections(ctx, cu.User.ID, s.googleConnector.ID())
	if err != nil {
		return store.AccountConnection{}, false, err
	}
	want := store.NormalizeContactEmail(identity.Email)
	for _, connection := range connections {
		if store.NormalizeContactEmail(connection.Email) != want {
			continue
		}
		gate, err := s.store.GetProviderSyncGate(ctx, cu.User.ID, string(provider.Gmail), googleSourceID(connection))
		if err != nil {
			return store.AccountConnection{}, true, err
		}
		if !gate.BackfillApproved() {
			return store.AccountConnection{}, true, errors.New("Gmail send remains locked pending Census review and explicit approval")
		}
		return connection, true, nil
	}
	return store.AccountConnection{}, false, nil
}

func (s *Server) buildProviderComposeRaw(ctx context.Context, cu currentUser, form composeForm, identity composeIdentity) ([]byte, error) {
	attachments, err := s.composeMessageAttachments(ctx, cu.User.ID, form)
	if err != nil {
		return nil, err
	}
	if form.SecurityEncrypted || form.SecuritySigned {
		if len(attachments) > 0 || form.AttachPublicKey {
			return nil, errors.New("message security does not support attachments yet")
		}
	} else if form.AttachPublicKey {
		attachment, err := s.composePublicKeyAttachment(ctx, cu.User.ID, identity)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	bodyHTML, bodyText := form.BodyHTML, form.Body
	if !form.SecurityEncrypted && !form.SecuritySigned {
		bodyHTML, bodyText = appendIdentitySignature(form.BodyHTML, form.Body, identity.Signature)
	}
	msg := smtpclient.Message{From: identity.Header, To: []string{form.To}, Cc: []string{form.Cc}, Bcc: []string{form.Bcc}, Subject: form.Subject, BodyText: bodyText, BodyHTML: bodyHTML, MessageID: smtpclient.NewMessageID(identity.Email), Date: time.Now().UTC(), Attachments: attachments}
	if err := s.applyPluginMIMEBodyOverride(ctx, cu.User.ID, &msg, identity, form); err != nil {
		return nil, err
	}
	s.applyPluginMailHeaders(ctx, cu.User.ID, &msg, identity)
	if form.InReplyToID > 0 {
		reply, err := s.store.GetMessageForUser(ctx, cu.User.ID, form.InReplyToID)
		if err != nil && !store.IsNotFound(err) {
			return nil, err
		}
		if err == nil {
			msg.InReplyTo = reply.MessageIDHeader
			msg.References = referencesForReply(reply)
		}
	}
	raw, _, err := smtpclient.BuildRaw(msg)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// queueProviderCompose returns handled=false for ordinary SMTP identities. Gmail
// jobs are durably spooled before they are exposed as queued.
func (s *Server) queueProviderCompose(ctx context.Context, cu currentUser, form composeForm) (store.ProviderOutboxJob, bool, error) {
	identity, err := s.selectedComposeIdentity(ctx, cu, form.FromIdentityID)
	if err != nil {
		return store.ProviderOutboxJob{}, false, err
	}
	connection, handled, err := s.providerComposeConnection(ctx, cu, form, identity)
	if err != nil || !handled {
		return store.ProviderOutboxJob{}, handled, err
	}
	form.SubmissionKey = strings.TrimSpace(form.SubmissionKey)
	if form.SubmissionKey == "" {
		form.SubmissionKey = smtpclient.NewSubmissionID()
	}
	if len(form.SubmissionKey) > 200 {
		return store.ProviderOutboxJob{}, true, errors.New("compose submission identifier is invalid")
	}
	sourceID := googleSourceID(connection)
	threadID := ""
	if form.InReplyToID > 0 {
		if meta, metaErr := s.store.ProviderMessageForLocalID(ctx, cu.User.ID, form.InReplyToID); metaErr == nil && meta.Provider == string(provider.Gmail) && meta.Account == sourceID {
			threadID = meta.ThreadID
		} else if metaErr != nil && !store.IsNotFound(metaErr) {
			return store.ProviderOutboxJob{}, true, metaErr
		}
	}
	if existing, err := s.store.GetProviderOutboxBySubmission(ctx, cu.User.ID, string(provider.Gmail), sourceID, form.SubmissionKey); err == nil {
		return existing, true, nil
	} else if !store.IsNotFound(err) {
		return store.ProviderOutboxJob{}, true, err
	}
	raw, err := s.buildProviderComposeRaw(ctx, cu, form, identity)
	if err != nil {
		return store.ProviderOutboxJob{}, true, err
	}
	saved, err := s.blobs.SaveOutboxMessage(cu.User.ID, form.SubmissionKey, raw)
	if err != nil {
		return store.ProviderOutboxJob{}, true, fmt.Errorf("save queued provider message: %w", err)
	}
	blobRec, err := s.store.CreateBlob(ctx, store.BlobRecord{UserID: cu.User.ID, Kind: "provider-outbox-message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size})
	if err != nil {
		_ = s.blobs.DeleteUserBlob(cu.User.ID, saved.Path)
		return store.ProviderOutboxJob{}, true, err
	}
	job, created, err := s.store.EnqueueProviderOutbox(ctx, store.ProviderOutboxEnqueue{UserID: cu.User.ID, Provider: string(provider.Gmail), SourceID: sourceID, SubmissionKey: form.SubmissionKey, Subject: strings.TrimSpace(form.Subject), ThreadID: threadID, BlobID: blobRec.ID, RawSHA256: saved.SHA256, RawSize: saved.Size})
	if err != nil {
		deleted, _ := s.store.DeleteBlobIfUnreferencedForUser(ctx, cu.User.ID, blobRec.ID)
		if deleted {
			_ = s.blobs.DeleteUserBlob(cu.User.ID, saved.Path)
		}
		return store.ProviderOutboxJob{}, true, err
	}
	if !created && blobRec.ID != job.BlobID {
		deleted, _ := s.store.DeleteBlobIfUnreferencedForUser(ctx, cu.User.ID, blobRec.ID)
		if deleted {
			_ = s.blobs.DeleteUserBlob(cu.User.ID, saved.Path)
		}
	}
	return job, true, nil
}

func providerOutboxResult(job store.ProviderOutboxJob, form composeForm) map[string]any {
	return map[string]any{"ok": true, "queued": true, "provider": job.Provider, "send_id": job.ID, "message_id": 0, "status": job.State, "to": form.To, "subject": form.Subject}
}

package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"rolltop/backend/mailai"
	"rolltop/backend/mailparse"
	"rolltop/backend/store"
)

const (
	mailAIAttachmentMaxCount   = 3
	mailAIAttachmentMaxBytes   = 2 << 20
	mailAIAttachmentTotalBytes = 4 << 20
	mailAIAttachmentTextBytes  = 64 << 10
	mailAIAttachmentTimeout    = 4 * time.Second
)

func (s *Server) runMailAIWorkspaceTool(r *http.Request, in mailai.ToolRequest) (mailai.ToolResponse, int, error) {
	if !in.Confirmed {
		return mailai.ToolResponse{}, http.StatusConflict, errors.New("explicit confirmation required")
	}
	var args struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		Detail    string `json:"detail"`
		MessageID int64  `json:"message_id"`
		Status    string `json:"status"`
		DueAt     string `json:"due_at"`
	}
	if err := json.Unmarshal(in.Arguments, &args); err != nil {
		return mailai.ToolResponse{}, 400, errors.New("invalid workspace arguments")
	}
	if args.MessageID > 0 {
		if _, err := s.store.GetMessageForUser(r.Context(), in.UserID, args.MessageID); err != nil {
			if store.IsNotFound(err) {
				return mailai.ToolResponse{}, 404, errors.New("message not found")
			}
			return mailai.ToolResponse{}, 500, err
		}
	}
	item, err := s.store.UpsertMailAIWorkspaceItem(r.Context(), in.UserID, store.MailAIWorkspaceItem{
		ID: strings.TrimSpace(args.ID), Kind: args.Kind, Title: args.Title,
		Detail: args.Detail, MessageID: args.MessageID, Status: args.Status, DueAt: args.DueAt,
	})
	if err != nil {
		return mailai.ToolResponse{}, 400, err
	}
	return mailai.ToolResponse{Name: in.Name, Result: item}, 200, nil
}

type mailAIAttachmentResult struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Text        string `json:"text,omitempty"`
	Data        string `json:"data,omitempty"`
}

func (s *Server) runMailAIAttachmentTool(r *http.Request, in mailai.ToolRequest) (mailai.ToolResponse, int, error) {
	var args struct {
		MessageID     int64   `json:"message_id"`
		AttachmentIDs []int64 `json:"attachment_ids"`
	}
	if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
		return mailai.ToolResponse{}, 400, errors.New("read_attachments requires message_id")
	}
	if _, err := s.store.GetMessageForUser(r.Context(), in.UserID, args.MessageID); err != nil {
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, 404, errors.New("message not found")
		}
		return mailai.ToolResponse{}, 500, err
	}
	attachments, err := s.store.ListAttachmentsForMessage(r.Context(), in.UserID, args.MessageID)
	if err != nil {
		return mailai.ToolResponse{}, 500, err
	}
	if len(args.AttachmentIDs) == 0 {
		items := make([]mailAIAttachmentResult, 0, len(attachments))
		for _, a := range attachments {
			items = append(items, mailAIAttachmentResult{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size})
		}
		return mailai.ToolResponse{Name: in.Name, Result: map[string]any{
			"attachments": items, "read_limits": map[string]any{"count": mailAIAttachmentMaxCount, "per_attachment_bytes": mailAIAttachmentMaxBytes, "total_bytes": mailAIAttachmentTotalBytes, "timeout_ms": mailAIAttachmentTimeout.Milliseconds()},
		}}, 200, nil
	}
	if len(args.AttachmentIDs) > mailAIAttachmentMaxCount {
		return mailai.ToolResponse{}, 400, errors.New("at most 3 attachments may be read")
	}
	ctx, cancel := context.WithTimeout(r.Context(), mailAIAttachmentTimeout)
	defer cancel()
	return s.readMailAIAttachments(ctx, in, args.MessageID, args.AttachmentIDs)
}

func (s *Server) readMailAIAttachments(ctx context.Context, in mailai.ToolRequest, messageID int64, ids []int64) (mailai.ToolResponse, int, error) {
	results := make([]mailAIAttachmentResult, 0, len(ids))
	var total int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return mailai.ToolResponse{}, 408, errors.New("attachment read timed out")
		}
		a, err := s.store.GetAttachmentForUser(ctx, in.UserID, id)
		if err != nil || a.MessageID != messageID {
			if store.IsNotFound(err) || a.MessageID != messageID {
				return mailai.ToolResponse{}, 404, errors.New("attachment not found")
			}
			return mailai.ToolResponse{}, 500, err
		}
		if a.Size > mailAIAttachmentMaxBytes || total+a.Size > mailAIAttachmentTotalBytes {
			return mailai.ToolResponse{}, 413, errors.New("attachment read limit exceeded")
		}
		data, err := s.readMailAIAttachmentBytes(ctx, a)
		if err != nil {
			return mailai.ToolResponse{}, 410, err
		}
		total += int64(len(data))
		item := mailAIAttachmentResult{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: int64(len(data))}
		parsed := mailparse.Attachment{Filename: a.Filename, ContentType: a.ContentType, Data: data}
		text := parsed.SearchableText()
		if len(text) > mailAIAttachmentTextBytes {
			text = text[:mailAIAttachmentTextBytes]
		}
		if text != "" {
			item.Text = text
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.ContentType)), "image/") {
			item.Data = base64.StdEncoding.EncodeToString(data)
		}
		results = append(results, item)
	}
	return mailai.ToolResponse{Name: in.Name, Result: map[string]any{"attachments": results}}, 200, nil
}

func (s *Server) readMailAIAttachmentBytes(ctx context.Context, a store.Attachment) ([]byte, error) {
	if strings.TrimSpace(a.BlobPath) == "" {
		msg, err := s.store.GetMessageForUser(ctx, a.UserID, a.MessageID)
		if err != nil {
			return nil, errors.New("attachment message is unavailable")
		}
		raw, err := s.rawMessageBytes(ctx, a.UserID, msg)
		if err != nil {
			return nil, errors.New("attachment body is unavailable")
		}
		parsed, err := mailparse.Parse(raw)
		if err != nil {
			return nil, errors.New("attachment body could not be parsed")
		}
		file, ok := matchingAttachment(a, parsed.Files)
		if !ok {
			return nil, errors.New("attachment body could not be matched")
		}
		if len(file.Data) > mailAIAttachmentMaxBytes {
			return nil, errors.New("attachment read limit exceeded")
		}
		return file.Data, nil
	}
	f, err := s.blobs.OpenUserBlob(a.UserID, a.BlobPath)
	if err != nil {
		return nil, errors.New("attachment body is unavailable")
	}
	defer f.Close()
	result := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(f, mailAIAttachmentMaxBytes+1))
		result <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, errors.New("attachment read timed out")
	case out := <-result:
		if out.err != nil {
			return nil, out.err
		}
		if len(out.data) > mailAIAttachmentMaxBytes {
			return nil, errors.New("attachment read limit exceeded")
		}
		return out.data, nil
	}
}

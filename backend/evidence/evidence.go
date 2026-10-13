// Package evidence persists immutable raw RFC822 objects and provider-neutral
// observations independently from the mutable mail mirror.
package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"rolltop/backend/provider"
	"rolltop/backend/store"
)

type Input struct {
	UserID            int64
	Provider          provider.Kind
	SourceAccount     string
	ProviderMessageID string
	ThreadID          string
	Folders           []string
	Labels            []string
	IMAPUID           *uint32
	IMAPUIDValidity   *uint32
	MessageIDHeader   string
	SourceSize        int64
	Raw               []byte
	Oversized         bool
}

type Service struct {
	Store *store.Store
	Root  string
}

func (s Service) Capture(ctx context.Context, in Input) (store.EvidenceObservation, error) {
	if s.Store == nil || strings.TrimSpace(s.Root) == "" {
		return store.EvidenceObservation{}, errors.New("evidence store and root are required")
	}
	if in.UserID <= 0 || in.Provider == "" || strings.TrimSpace(in.SourceAccount) == "" || strings.TrimSpace(in.ProviderMessageID) == "" {
		return store.EvidenceObservation{}, errors.New("invalid evidence capture scope")
	}
	folders := canonicalStrings(in.Folders)
	labels := canonicalStrings(in.Labels)
	foldersJSON, _ := json.Marshal(folders)
	labelsJSON, _ := json.Marshal(labels)
	base := store.EvidenceObservationInput{
		UserID: in.UserID, Provider: string(in.Provider), SourceAccount: in.SourceAccount, ProviderMessageID: in.ProviderMessageID,
		ThreadID: in.ThreadID, FoldersJSON: string(foldersJSON), LabelsJSON: string(labelsJSON), IMAPUID: in.IMAPUID,
		IMAPUIDValidity: in.IMAPUIDValidity, MessageIDHeader: in.MessageIDHeader, SourceSize: in.SourceSize,
	}
	if in.Oversized {
		base.CaptureStatus = store.EvidenceCaptureOversized
		base.ObservationKey = observationKey(base)
		obs, _, err := s.Store.RecordEvidenceObservation(ctx, base)
		return obs, err
	}
	if len(in.Raw) == 0 {
		return s.recordFailure(ctx, base, errors.New("complete evidence capture requires raw RFC822 bytes"))
	}
	saved, err := saveRaw(s.Root, in.Raw)
	if err != nil {
		return s.recordFailure(ctx, base, fmt.Errorf("save evidence raw: %w", err))
	}
	base.CaptureStatus = store.EvidenceCaptureComplete
	base.RawSHA256 = saved.SHA256
	base.RawPath = saved.Path
	base.RawSize = saved.Size
	base.ObservationKey = observationKey(base)
	obs, _, err := s.Store.RecordEvidenceObservation(ctx, base)
	return obs, err
}

func (s Service) recordFailure(ctx context.Context, base store.EvidenceObservationInput, captureErr error) (store.EvidenceObservation, error) {
	base.CaptureStatus = store.EvidenceCaptureFailed
	base.LastError = captureErr.Error()
	base.ObservationKey = observationKey(base)
	obs, _, recordErr := s.Store.RecordEvidenceObservation(ctx, base)
	return obs, errors.Join(captureErr, recordErr)
}

type savedRaw struct {
	Path   string
	SHA256 string
	Size   int64
}

func saveRaw(root string, raw []byte) (savedRaw, error) {
	if len(raw) == 0 {
		return savedRaw{}, errors.New("empty raw evidence")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	rel := filepath.Join("evidence", "mail", "sha256", hash[:2], hash[2:4], hash+".eml")
	abs := filepath.Join(root, rel)
	if info, err := os.Stat(abs); err == nil {
		if info.Size() != int64(len(raw)) || !info.Mode().IsRegular() {
			return savedRaw{}, errors.New("existing evidence object does not match expected size")
		}
		return savedRaw{Path: rel, SHA256: hash, Size: info.Size()}, nil
	} else if !os.IsNotExist(err) {
		return savedRaw{}, err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return savedRaw{}, err
	}
	tmp, err := os.CreateTemp(dir, ".evidence-*.tmp")
	if err != nil {
		return savedRaw{}, err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return savedRaw{}, err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(raw)); err != nil {
		cleanup()
		return savedRaw{}, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return savedRaw{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return savedRaw{}, err
	}
	if err := os.Rename(name, abs); err != nil {
		_ = os.Remove(name)
		return savedRaw{}, err
	}
	if err := syncDir(dir); err != nil {
		return savedRaw{}, err
	}
	return savedRaw{Path: rel, SHA256: hash, Size: int64(len(raw))}, nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

func observationKey(in store.EvidenceObservationInput) string {
	material := struct {
		Provider, SourceAccount, ProviderMessageID, ThreadID, FoldersJSON, LabelsJSON, MessageIDHeader string
		IMAPUID, IMAPUIDValidity                                                                       *uint32
		SourceSize                                                                                     int64
		RawSHA256, CaptureStatus                                                                       string
	}{in.Provider, in.SourceAccount, in.ProviderMessageID, in.ThreadID, in.FoldersJSON, in.LabelsJSON, in.MessageIDHeader,
		in.IMAPUID, in.IMAPUIDValidity, in.SourceSize, in.RawSHA256, in.CaptureStatus}
	data, _ := json.Marshal(material)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

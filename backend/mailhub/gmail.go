package mailhub

import (
	"context"
	"errors"
	"strings"

	"rolltop/backend/provider"
	gmailprovider "rolltop/backend/provider/gmail"
)

// SyncGmail projects a Gmail account through the same provider-neutral Mirror
// pipeline used by synthetic sources. An expired history cursor is explicit:
// persistently clear it first, then run a full snapshot reconciliation. A crash
// between those steps safely resumes with a full reconciliation next time.
func (s Service) SyncGmail(ctx context.Context, a provider.Account, label, email string, client gmailprovider.Client) error {
	if a.Provider != provider.Gmail || a.UserID <= 0 || strings.TrimSpace(a.ID) == "" || strings.TrimSpace(email) == "" {
		return errors.New("gmail sync requires a scoped account identity")
	}
	if strings.TrimSpace(label) == "" {
		label = email
	}
	p := gmailprovider.Provider{Client: client, Account: a}
	spec := providerSourceSpec{Provider: provider.Gmail, ID: a.ID, Label: label, Email: email}
	err := s.syncProviderSource(ctx, a, spec, p)
	if !errors.Is(err, gmailprovider.ErrHistoryExpired) {
		return err
	}
	if clearErr := s.Store.SetProviderCursorFor(context.WithoutCancel(ctx), a.UserID, string(provider.Gmail), a.ID, ""); clearErr != nil {
		return errors.Join(err, clearErr)
	}
	return s.syncProviderSource(ctx, a, spec, p)
}

// MutateGmail writes the source first, then refreshes the Mirror from native
// history. If projection fails the provider cursor is unchanged, so retry is
// deterministic and source truth wins.
func (s Service) MutateGmail(ctx context.Context, a provider.Account, label, email string, client gmailprovider.Client, change provider.Mutation) error {
	if err := client.Mutate(ctx, change); err != nil {
		return err
	}
	return s.SyncGmail(ctx, a, label, email, client)
}

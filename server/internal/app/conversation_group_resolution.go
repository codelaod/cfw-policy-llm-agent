package app

import (
	"context"

	"cfw-policy-llm-agent/server/internal/storage"
)

// A trusted group has a finite, versioned membership. Traverse every local
// page under its original catalog cursor; never promote the first page to the
// whole group. An ordinary name lookup still requires explicit disambiguation.
func (s *IntentService) allConfirmedGroupResources(ctx context.Context, filter storage.ResourceFilter, versionID string) (storage.ResourcePage, error) {
	v, e := s.Store.ResourceGroupVersion(ctx, versionID)
	if e != nil {
		return storage.ResourcePage{}, e
	}
	bound := len(v.Members)
	combined := storage.ResourcePage{}
	cursor := ""
	for {
		page, e := s.Store.SearchResources(ctx, filter, cursor, 100)
		if e != nil {
			return combined, e
		}
		combined.Items = append(combined.Items, page.Items...)
		combined.Coverage = page.Coverage
		if len(combined.Items) > bound {
			return combined, storage.ErrPlanStale
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return combined, storage.ErrCursor
		}
		cursor = page.NextCursor
	}
	head, e := s.Store.ResourceGroup(ctx, v.SourceRef, v.AccountRef, v.GroupName)
	if e != nil {
		return combined, e
	}
	if head.ID != versionID {
		return combined, storage.ErrPlanStale
	}
	return combined, nil
}

// allFiniteResources reads a finite local selector under stable catalog cursors.
// Complete enumeration from one source/account is required before selecting all.
// The current storage envelope bounds this read; reaching it keeps HasMore true.
func (s *IntentService) allFiniteResources(ctx context.Context, filter storage.ResourceFilter) (storage.ResourcePage, error) {
	read := func(f storage.ResourceFilter) (storage.ResourcePage, error) {
		combined := storage.ResourcePage{}
		cursor := ""
		for {
			page, err := s.Store.SearchResources(ctx, f, cursor, 100)
			if err != nil {
				return combined, err
			}
			combined.Items = append(combined.Items, page.Items...)
			combined.Coverage = page.Coverage
			combined.HasMore, combined.NextCursor = page.HasMore, page.NextCursor
			if !page.HasMore || len(combined.Items) >= storage.MaxResourceBindings {
				return combined, nil
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				return combined, storage.ErrCursor
			}
			cursor = page.NextCursor
		}
	}
	page, err := read(filter)
	if err != nil || page.HasMore || len(page.Items) == 0 {
		return page, err
	}
	source, account := page.Items[0].SourceRef, page.Items[0].AccountRef
	for _, r := range page.Items {
		if r.SourceRef != source || r.AccountRef != account {
			page.Coverage.Complete = false
			return page, nil
		}
	}
	if filter.SourceRef == "" || filter.AccountRef == "" {
		filter.SourceRef, filter.AccountRef = source, account
		scoped, err := read(filter)
		if err != nil {
			return scoped, err
		}
		if scoped.Coverage.Revision != page.Coverage.Revision || len(scoped.Items) != len(page.Items) {
			return scoped, storage.ErrCursor
		}
		page = scoped
	}
	return page, nil
}
